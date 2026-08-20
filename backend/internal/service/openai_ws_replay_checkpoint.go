package service

import (
	"container/list"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	openAIWSReplayCheckpointMaxEntries           = 65536
	openAIWSReplayCheckpointMaxNodeBytes         = 32 << 20
	openAIWSReplayCheckpointMaxMaterializedBytes = 32 << 20
	openAIWSReplayCheckpointMaxTotalBytes        = 256 << 20
	openAIWSReplayCheckpointMaxDepth             = 1024
)

// openAIWSReplayCheckpoint 保存一轮响应的增量上下文；FullInput 仅在读取物化后返回。
type openAIWSReplayCheckpoint struct {
	SourceConnID       string
	Target             openAIWSConnectionTarget
	PreviousResponseID string
	RequestInput       []json.RawMessage
	RequestInputSeen   bool
	ResponseOutput     []json.RawMessage
	Replayable         bool
	UnavailableReason  string
	FullInput          []json.RawMessage
	FullInputExists    bool
}

type openAIWSReplayCheckpointStateStore interface {
	BindReplayCheckpoint(groupID int64, responseID string, checkpoint openAIWSReplayCheckpoint, ttl time.Duration)
	GetReplayCheckpoint(groupID int64, responseID string, expected openAIWSConnectionTarget) (openAIWSReplayCheckpoint, bool)
}

type openAIWSReplayCheckpointBinding struct {
	checkpoint openAIWSReplayCheckpoint
	expiresAt  time.Time
	bytes      int64
	lruElement *list.Element
}

type openAIWSReplayCheckpointCache struct {
	mu         sync.Mutex
	bindings   map[string]*openAIWSReplayCheckpointBinding
	lru        *list.List
	totalBytes int64
}

func newOpenAIWSReplayCheckpointCache() *openAIWSReplayCheckpointCache {
	return &openAIWSReplayCheckpointCache{
		bindings: make(map[string]*openAIWSReplayCheckpointBinding, 256),
		lru:      list.New(),
	}
}

func (s *defaultOpenAIWSStateStore) BindReplayCheckpoint(groupID int64, responseID string, checkpoint openAIWSReplayCheckpoint, ttl time.Duration) {
	if s == nil || s.replayCheckpoints == nil {
		return
	}
	s.replayCheckpoints.bind(groupID, responseID, checkpoint, ttl)
}

func (s *defaultOpenAIWSStateStore) GetReplayCheckpoint(groupID int64, responseID string, expected openAIWSConnectionTarget) (openAIWSReplayCheckpoint, bool) {
	if s == nil || s.replayCheckpoints == nil {
		return openAIWSReplayCheckpoint{}, false
	}
	return s.replayCheckpoints.get(groupID, responseID, expected)
}

func bindOpenAIWSReplayCheckpoint(store OpenAIWSStateStore, groupID int64, responseID string, checkpoint openAIWSReplayCheckpoint, ttl time.Duration) {
	replayStore, ok := store.(openAIWSReplayCheckpointStateStore)
	if !ok {
		return
	}
	replayStore.BindReplayCheckpoint(groupID, responseID, checkpoint, ttl)
}

func getOpenAIWSReplayCheckpoint(store OpenAIWSStateStore, groupID int64, responseID string, expected openAIWSConnectionTarget) (openAIWSReplayCheckpoint, bool) {
	replayStore, ok := store.(openAIWSReplayCheckpointStateStore)
	if !ok {
		return openAIWSReplayCheckpoint{}, false
	}
	return replayStore.GetReplayCheckpoint(groupID, responseID, expected)
}

func (c *openAIWSReplayCheckpointCache) bind(groupID int64, responseID string, checkpoint openAIWSReplayCheckpoint, ttl time.Duration) {
	if c == nil {
		return
	}
	key := openAIWSReplayCheckpointMapKey(groupID, responseID)
	if key == "" || !checkpoint.Target.valid() {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	checkpoint.SourceConnID = strings.TrimSpace(checkpoint.SourceConnID)
	checkpoint.PreviousResponseID = normalizeOpenAIWSResponseID(checkpoint.PreviousResponseID)
	checkpoint.FullInput = nil
	checkpoint.FullInputExists = false
	checkpoint.RequestInput = cloneOpenAIWSRawMessages(checkpoint.RequestInput)
	checkpoint.ResponseOutput = cloneOpenAIWSRawMessages(checkpoint.ResponseOutput)

	nodeBytes := openAIWSRawMessagesBytes(checkpoint.RequestInput) + openAIWSRawMessagesBytes(checkpoint.ResponseOutput)
	if nodeBytes > openAIWSReplayCheckpointMaxNodeBytes {
		checkpoint.RequestInput = nil
		checkpoint.ResponseOutput = nil
		checkpoint.Replayable = false
		checkpoint.UnavailableReason = "node_too_large"
		nodeBytes = 0
	}
	if !checkpoint.Replayable && strings.TrimSpace(checkpoint.UnavailableReason) == "" {
		checkpoint.UnavailableReason = "checkpoint_incomplete"
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.cleanupExpiredLocked(now, openAIWSStateStoreCleanupMaxPerMap)
	if existing := c.bindings[key]; existing != nil {
		c.removeLocked(key, existing)
	}
	for len(c.bindings) >= openAIWSReplayCheckpointMaxEntries {
		if !c.removeOldestLocked() {
			break
		}
	}
	for c.totalBytes+nodeBytes > openAIWSReplayCheckpointMaxTotalBytes {
		if !c.degradeOldestLocked() {
			checkpoint.RequestInput = nil
			checkpoint.ResponseOutput = nil
			checkpoint.Replayable = false
			checkpoint.UnavailableReason = "capacity_exceeded"
			nodeBytes = 0
			break
		}
	}

	binding := &openAIWSReplayCheckpointBinding{
		checkpoint: checkpoint,
		expiresAt:  now.Add(ttl),
		bytes:      nodeBytes,
	}
	binding.lruElement = c.lru.PushFront(key)
	c.bindings[key] = binding
	c.totalBytes += nodeBytes
}

func (c *openAIWSReplayCheckpointCache) get(groupID int64, responseID string, expected openAIWSConnectionTarget) (openAIWSReplayCheckpoint, bool) {
	if c == nil {
		return openAIWSReplayCheckpoint{}, false
	}
	key := openAIWSReplayCheckpointMapKey(groupID, responseID)
	if key == "" {
		return openAIWSReplayCheckpoint{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	requested := c.bindings[key]
	if requested == nil || now.After(requested.expiresAt) {
		if requested != nil {
			c.removeLocked(key, requested)
		}
		return openAIWSReplayCheckpoint{}, false
	}
	base := requested.checkpoint
	base.RequestInput = nil
	base.ResponseOutput = nil
	base.FullInput = nil
	base.FullInputExists = false
	if !base.Target.matches(expected) {
		base.Replayable = false
		base.UnavailableReason = "target_mismatch"
		return base, true
	}

	chain := make([]*openAIWSReplayCheckpointBinding, 0, 8)
	visited := make(map[string]struct{}, 8)
	cursorKey := key
	for depth := 0; cursorKey != ""; depth++ {
		if depth >= openAIWSReplayCheckpointMaxDepth {
			base.Replayable = false
			base.UnavailableReason = "chain_too_deep"
			return base, true
		}
		if _, exists := visited[cursorKey]; exists {
			base.Replayable = false
			base.UnavailableReason = "checkpoint_cycle"
			return base, true
		}
		visited[cursorKey] = struct{}{}
		binding := c.bindings[cursorKey]
		if binding == nil || now.After(binding.expiresAt) {
			if binding != nil {
				c.removeLocked(cursorKey, binding)
			}
			base.Replayable = false
			base.UnavailableReason = "missing_parent_checkpoint"
			return base, true
		}
		if !binding.checkpoint.Target.matches(expected) {
			base.Replayable = false
			base.UnavailableReason = "target_mismatch"
			return base, true
		}
		if !binding.checkpoint.Replayable {
			base.Replayable = false
			base.UnavailableReason = strings.TrimSpace(binding.checkpoint.UnavailableReason)
			if base.UnavailableReason == "" {
				base.UnavailableReason = "checkpoint_incomplete"
			}
			return base, true
		}
		chain = append(chain, binding)
		parentID := normalizeOpenAIWSResponseID(binding.checkpoint.PreviousResponseID)
		if parentID == "" {
			break
		}
		cursorKey = openAIWSReplayCheckpointMapKey(groupID, parentID)
	}

	fullInput := make([]json.RawMessage, 0, 16)
	fullInputExists := false
	materializedBytes := int64(0)
	for idx := len(chain) - 1; idx >= 0; idx-- {
		checkpoint := chain[idx].checkpoint
		if checkpoint.RequestInputSeen {
			requestInput := cloneOpenAIWSRawMessages(checkpoint.RequestInput)
			if !fullInputExists || !openAIWSRawItemsHasPrefix(requestInput, fullInput) {
				fullInput = append(fullInput, requestInput...)
			} else {
				fullInput = requestInput
			}
			fullInputExists = true
		}
		if len(checkpoint.ResponseOutput) > 0 {
			fullInput = append(fullInput, cloneOpenAIWSRawMessages(checkpoint.ResponseOutput)...)
			fullInputExists = true
		}
		materializedBytes = openAIWSRawMessagesBytes(fullInput)
		if materializedBytes > openAIWSReplayCheckpointMaxMaterializedBytes {
			base.Replayable = false
			base.UnavailableReason = "chain_too_large"
			return base, true
		}
	}
	for _, binding := range chain {
		if binding.lruElement != nil {
			c.lru.MoveToFront(binding.lruElement)
		}
	}
	base.Replayable = true
	base.UnavailableReason = ""
	base.FullInput = fullInput
	base.FullInputExists = fullInputExists
	return base, true
}

func (c *openAIWSReplayCheckpointCache) cleanupExpired(now time.Time, maxScan int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.cleanupExpiredLocked(now, maxScan)
	c.mu.Unlock()
}

func (c *openAIWSReplayCheckpointCache) cleanupExpiredLocked(now time.Time, maxScan int) {
	if maxScan <= 0 {
		return
	}
	scanned := 0
	for element := c.lru.Back(); element != nil && scanned < maxScan; {
		previous := element.Prev()
		key, _ := element.Value.(string)
		binding := c.bindings[key]
		if binding == nil || now.After(binding.expiresAt) {
			c.removeLocked(key, binding)
		}
		element = previous
		scanned++
	}
}

func (c *openAIWSReplayCheckpointCache) degradeOldestLocked() bool {
	for element := c.lru.Back(); element != nil; element = element.Prev() {
		key, _ := element.Value.(string)
		binding := c.bindings[key]
		if binding == nil || binding.bytes <= 0 {
			continue
		}
		c.totalBytes -= binding.bytes
		binding.bytes = 0
		binding.checkpoint.RequestInput = nil
		binding.checkpoint.ResponseOutput = nil
		binding.checkpoint.Replayable = false
		binding.checkpoint.UnavailableReason = "capacity_evicted"
		return true
	}
	return false
}

func (c *openAIWSReplayCheckpointCache) removeOldestLocked() bool {
	element := c.lru.Back()
	if element == nil {
		return false
	}
	key, _ := element.Value.(string)
	c.removeLocked(key, c.bindings[key])
	return true
}

func (c *openAIWSReplayCheckpointCache) removeLocked(key string, binding *openAIWSReplayCheckpointBinding) {
	if binding != nil {
		c.totalBytes -= binding.bytes
		if c.totalBytes < 0 {
			c.totalBytes = 0
		}
		if binding.lruElement != nil {
			c.lru.Remove(binding.lruElement)
		}
	}
	delete(c.bindings, key)
}

func openAIWSReplayCheckpointMapKey(groupID int64, responseID string) string {
	responseID = normalizeOpenAIWSResponseID(responseID)
	if responseID == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s", groupID, responseID)
}

func openAIWSRawMessagesBytes(items []json.RawMessage) int64 {
	var total int64
	for _, item := range items {
		total += int64(len(item))
	}
	return total
}
