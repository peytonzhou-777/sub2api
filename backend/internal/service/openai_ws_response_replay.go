package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// openAIWSResponseReplayCollector 收集可作为后续 response.create input 的有序输出项。
type openAIWSResponseReplayCollector struct {
	items             []json.RawMessage
	seen              map[string]struct{}
	unavailableReason string
}

func (c *openAIWSResponseReplayCollector) AddEvent(eventType string, message []byte) {
	if c == nil {
		return
	}
	switch strings.TrimSpace(eventType) {
	case "response.output_item.done":
		c.addItem(gjson.GetBytes(message, "item"))
	case "response.completed", "response.done":
		output := gjson.GetBytes(message, "response.output")
		if !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			c.addItem(item)
		}
	}
}

func (c *openAIWSResponseReplayCollector) Items() []json.RawMessage {
	if c == nil {
		return nil
	}
	return cloneOpenAIWSRawMessages(c.items)
}

func (c *openAIWSResponseReplayCollector) Replayable() (bool, string) {
	if c == nil || strings.TrimSpace(c.unavailableReason) == "" {
		return true, ""
	}
	return false, strings.TrimSpace(c.unavailableReason)
}

func (c *openAIWSResponseReplayCollector) addItem(item gjson.Result) {
	if !item.Exists() || item.Type != gjson.JSON {
		c.markUnavailable("invalid_output_item")
		return
	}
	raw := strings.TrimSpace(item.Raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		c.markUnavailable("invalid_output_item")
		return
	}
	itemType := strings.TrimSpace(item.Get("type").String())
	if !isOpenAIWSReplayableResponseOutputItem(itemType, item) {
		reason := "unsupported_output_item"
		if itemType != "" {
			reason += ":" + itemType
		}
		c.markUnavailable(reason)
		return
	}
	key := itemType + ":" + strings.TrimSpace(item.Get("id").String())
	if strings.HasSuffix(key, ":") {
		key += strings.TrimSpace(item.Get("call_id").String())
	}
	if strings.HasSuffix(key, ":") {
		key += raw
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	c.items = append(c.items, json.RawMessage(raw))
}

func (c *openAIWSResponseReplayCollector) markUnavailable(reason string) {
	if c == nil || c.unavailableReason != "" {
		return
	}
	c.unavailableReason = strings.TrimSpace(reason)
}

func isOpenAIWSReplayableResponseOutputItem(itemType string, item gjson.Result) bool {
	itemType = strings.TrimSpace(itemType)
	if isCodexToolCallContextItemType(itemType) {
		return true
	}
	switch itemType {
	case "message":
		return strings.TrimSpace(item.Get("role").String()) == "assistant"
	case "reasoning", "compaction":
		// 无加密内容的推理项不能在新连接上恢复内部状态。
		return strings.TrimSpace(item.Get("encrypted_content").String()) != ""
	default:
		return false
	}
}
