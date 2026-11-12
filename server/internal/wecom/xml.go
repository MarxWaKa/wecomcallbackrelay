package wecom

import (
	"crypto/sha1"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// BatchJob 异步任务完成通知（Event=batch_job_result）中的任务结果。
type BatchJob struct {
	JobID   string `xml:"JobId"`
	JobType string `xml:"JobType"`
	ErrCode int    `xml:"ErrCode"`
	ErrMsg  string `xml:"ErrMsg"`
}

// CallbackMessage 企微通讯录回调明文 XML。
// 字段命名与官方文档保持一致：UserID/NewUserID/Id/ParentId/TagId 等大小写敏感。
type CallbackMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Event        string   `xml:"Event"`
	ChangeType   string   `xml:"ChangeType"`
	UserID       string   `xml:"UserID"`
	NewUserID    string   `xml:"NewUserID"`
	Department   string   `xml:"Department"`
	ID           string   `xml:"Id"`
	ParentID     string   `xml:"ParentId"`
	MsgID        string   `xml:"MsgId"`
	ReqID        string   `xml:"ReqId"`

	// 标签变更通知（ChangeType=update_tag）
	TagID         string `xml:"TagId"`
	AddUserItems  string `xml:"AddUserItems"`
	DelUserItems  string `xml:"DelUserItems"`
	AddPartyItems string `xml:"AddPartyItems"`
	DelPartyItems string `xml:"DelPartyItems"`

	// 异步任务完成通知（Event=batch_job_result）
	BatchJob *BatchJob `xml:"BatchJob"`
}

// Event 是 JSON 模式下发给消费端的标准载荷。
type Event struct {
	EventID      string         `json:"event_id"`
	Source       string         `json:"source"`
	EventType    string         `json:"event_type"`
	ChangeType   string         `json:"change_type"`
	CorpID       string         `json:"corp_id"`
	CreateTime   int64          `json:"create_time"`
	MsgID        string         `json:"msg_id,omitempty"`
	ReqID        string         `json:"req_id,omitempty"`
	ToUserName   string         `json:"to_user_name,omitempty"`
	FromUserName string         `json:"from_user_name,omitempty"`
	Payload      map[string]any `json:"payload"`
}

// ParseCallbackMessage 解析企微回调明文 XML。
func ParseCallbackMessage(raw string) (*CallbackMessage, error) {
	var msg CallbackMessage
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		return nil, fmt.Errorf("解析回调 XML 失败: %w", err)
	}
	return &msg, nil
}

// MakeEventID 生成稳定的事件 ID：优先使用 MsgId，其次使用明文哈希，保证企微重试时幂等。
func MakeEventID(msg *CallbackMessage, rawXML string) string {
	if msg.MsgID != "" {
		id := "evt_" + msg.MsgID
		if len(id) > 100 {
			id = id[:100]
		}
		return id
	}
	sum := sha1.Sum([]byte(rawXML))
	return fmt.Sprintf("evt_%d_%x", msg.CreateTime, sum[:8])
}

// BuildEvent 将企微 XML 转为 JSON 模式载荷；本服务只转发回调实际携带的 ID 字段。
func BuildEvent(msg *CallbackMessage, corpID, eventID string) Event {
	payload := map[string]any{}
	if msg.UserID != "" {
		payload["user_id"] = msg.UserID
	}
	if msg.NewUserID != "" {
		payload["new_user_id"] = msg.NewUserID
	}
	if msg.Department != "" {
		payload["department"] = parseDepartments(msg.Department)
	}
	if msg.ID != "" {
		payload["id"] = numberOrString(msg.ID)
	}
	if msg.ParentID != "" {
		payload["parent_id"] = numberOrString(msg.ParentID)
	}

	// 标签变更：TagId 及逗号分隔的成员/部门列表。
	if msg.TagID != "" {
		payload["tag_id"] = numberOrString(msg.TagID)
	}
	if msg.AddUserItems != "" {
		payload["add_user_items"] = splitCSVStrings(msg.AddUserItems)
	}
	if msg.DelUserItems != "" {
		payload["del_user_items"] = splitCSVStrings(msg.DelUserItems)
	}
	if msg.AddPartyItems != "" {
		payload["add_party_items"] = parseDepartments(msg.AddPartyItems)
	}
	if msg.DelPartyItems != "" {
		payload["del_party_items"] = parseDepartments(msg.DelPartyItems)
	}

	// 异步任务完成通知：Event=batch_job_result。
	if msg.BatchJob != nil {
		payload["job_id"] = msg.BatchJob.JobID
		payload["job_type"] = msg.BatchJob.JobType
		payload["err_code"] = msg.BatchJob.ErrCode
		payload["err_msg"] = msg.BatchJob.ErrMsg
	}

	return Event{
		EventID:      eventID,
		Source:       "wecom_contact_sync",
		EventType:    msg.Event,
		ChangeType:   msg.ChangeType,
		CorpID:       corpID,
		CreateTime:   msg.CreateTime,
		MsgID:        msg.MsgID,
		ReqID:        msg.ReqID,
		ToUserName:   msg.ToUserName,
		FromUserName: msg.FromUserName,
		Payload:      payload,
	}
}

// MarshalEvent 序列化 JSON 载荷。
func MarshalEvent(event Event) ([]byte, error) {
	return json.Marshal(event)
}

func parseDepartments(raw string) []int64 {
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func splitCSVStrings(raw string) []string {
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			items = append(items, v)
		}
	}
	return items
}

func numberOrString(raw string) any {
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	return raw
}
