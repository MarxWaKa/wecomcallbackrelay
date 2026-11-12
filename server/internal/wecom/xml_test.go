package wecom

import (
	"encoding/json"
	"testing"
)

const createUserXML = `<xml><ToUserName><![CDATA[ww123456]]></ToUserName><FromUserName><![CDATA[sys]]></FromUserName><CreateTime>1699999999</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[change_contact]]></Event><ChangeType>create_user</ChangeType><UserID><![CDATA[zhangsan]]></UserID><Department><![CDATA[1,2]]></Department><MsgId>123456789</MsgId></xml>`

func TestBuildEventCreateUser(t *testing.T) {
	msg, err := ParseCallbackMessage(createUserXML)
	if err != nil {
		t.Fatalf("解析 XML 失败: %v", err)
	}
	event := BuildEvent(msg, "ww123456", MakeEventID(msg, createUserXML))

	if event.Source != "wecom_contact_sync" || event.EventType != "change_contact" || event.ChangeType != "create_user" {
		t.Fatalf("事件基础字段错误: %+v", event)
	}
	if event.CorpID != "ww123456" || event.CreateTime != 1699999999 || event.MsgID != "123456789" {
		t.Fatalf("事件通用字段错误: %+v", event)
	}
	if event.Payload["user_id"] != "zhangsan" {
		t.Fatalf("user_id 映射错误: %+v", event.Payload)
	}
	dept, ok := event.Payload["department"].([]int64)
	if !ok || len(dept) != 2 || dept[0] != 1 || dept[1] != 2 {
		t.Fatalf("department 映射错误: %#v", event.Payload["department"])
	}
	if event.EventID != "evt_123456789" {
		t.Fatalf("event_id 应优先使用 MsgId: %q", event.EventID)
	}

	raw, err := MarshalEvent(event)
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("事件 JSON 非法: %v", err)
	}
	if decoded["event_id"] != "evt_123456789" {
		t.Fatalf("JSON event_id 错误: %v", decoded["event_id"])
	}
}

func TestBuildEventCreateParty(t *testing.T) {
	raw := `<xml><ToUserName><![CDATA[ww123456]]></ToUserName><CreateTime>1699999999</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[change_contact]]></Event><ChangeType>create_party</ChangeType><Id>5</Id><ParentId>0</ParentId></xml>`
	msg, err := ParseCallbackMessage(raw)
	if err != nil {
		t.Fatalf("解析 XML 失败: %v", err)
	}
	event := BuildEvent(msg, "ww123456", MakeEventID(msg, raw))
	if event.Payload["id"] != int64(5) || event.Payload["parent_id"] != int64(0) {
		t.Fatalf("部门字段映射错误: %#v", event.Payload)
	}
}

func TestMakeEventIDWithoutMsgIDIsStable(t *testing.T) {
	raw := `<xml><CreateTime>1699999999</CreateTime><Event>change_contact</Event></xml>`
	msg, _ := ParseCallbackMessage(raw)
	id1 := MakeEventID(msg, raw)
	id2 := MakeEventID(msg, raw)
	if id1 != id2 {
		t.Fatalf("同一明文生成的事件 ID 应稳定: %q != %q", id1, id2)
	}
}

func TestBuildEventUpdateTag(t *testing.T) {
	raw := `<xml><ToUserName><![CDATA[ww123456]]></ToUserName><FromUserName><![CDATA[sys]]></FromUserName><CreateTime>1699999999</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[change_contact]]></Event><ChangeType><![CDATA[update_tag]]></ChangeType><TagId>1</TagId><AddUserItems><![CDATA[zhangsan,lisi]]></AddUserItems><DelUserItems><![CDATA[zhangsan1,lisi1]]></DelUserItems><AddPartyItems><![CDATA[1,2]]></AddPartyItems><DelPartyItems><![CDATA[3,4]]></DelPartyItems></xml>`
	msg, err := ParseCallbackMessage(raw)
	if err != nil {
		t.Fatalf("解析标签事件失败: %v", err)
	}
	event := BuildEvent(msg, "ww123456", MakeEventID(msg, raw))

	if event.EventType != "change_contact" || event.ChangeType != "update_tag" {
		t.Fatalf("标签事件基础字段错误: %+v", event)
	}
	if event.FromUserName != "sys" {
		t.Fatalf("from_user_name 映射错误: %q", event.FromUserName)
	}
	if event.Payload["tag_id"] != int64(1) {
		t.Fatalf("tag_id 映射错误: %#v", event.Payload["tag_id"])
	}
	addUsers, ok := event.Payload["add_user_items"].([]string)
	if !ok || len(addUsers) != 2 || addUsers[0] != "zhangsan" || addUsers[1] != "lisi" {
		t.Fatalf("add_user_items 映射错误: %#v", event.Payload["add_user_items"])
	}
	delUsers, ok := event.Payload["del_user_items"].([]string)
	if !ok || len(delUsers) != 2 || delUsers[0] != "zhangsan1" || delUsers[1] != "lisi1" {
		t.Fatalf("del_user_items 映射错误: %#v", event.Payload["del_user_items"])
	}
	addParties, ok := event.Payload["add_party_items"].([]int64)
	if !ok || len(addParties) != 2 || addParties[0] != 1 || addParties[1] != 2 {
		t.Fatalf("add_party_items 映射错误: %#v", event.Payload["add_party_items"])
	}
	delParties, ok := event.Payload["del_party_items"].([]int64)
	if !ok || len(delParties) != 2 || delParties[0] != 3 || delParties[1] != 4 {
		t.Fatalf("del_party_items 映射错误: %#v", event.Payload["del_party_items"])
	}
}

func TestBuildEventBatchJobResult(t *testing.T) {
	raw := `<xml><ToUserName><![CDATA[ww123456]]></ToUserName><FromUserName><![CDATA[FromUser]]></FromUserName><CreateTime>1425284517</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[batch_job_result]]></Event><BatchJob><JobId><![CDATA[S0MrnndvRG5fadSlLwiBqiDDbM143UqTmKP3152FZk4]]></JobId><JobType><![CDATA[sync_user]]></JobType><ErrCode>0</ErrCode><ErrMsg><![CDATA[ok]]></ErrMsg></BatchJob></xml>`
	msg, err := ParseCallbackMessage(raw)
	if err != nil {
		t.Fatalf("解析异步任务事件失败: %v", err)
	}
	event := BuildEvent(msg, "ww123456", MakeEventID(msg, raw))

	if event.EventType != "batch_job_result" || event.ChangeType != "" {
		t.Fatalf("异步任务事件基础字段错误: %+v", event)
	}
	if event.FromUserName != "FromUser" {
		t.Fatalf("from_user_name 映射错误: %q", event.FromUserName)
	}
	if event.Payload["job_id"] != "S0MrnndvRG5fadSlLwiBqiDDbM143UqTmKP3152FZk4" ||
		event.Payload["job_type"] != "sync_user" ||
		event.Payload["err_code"] != 0 ||
		event.Payload["err_msg"] != "ok" {
		t.Fatalf("BatchJob 映射错误: %#v", event.Payload)
	}
}

func TestParseInvalidXML(t *testing.T) {
	if _, err := ParseCallbackMessage("<xml><broken>"); err == nil {
		t.Fatal("非法 XML 应解析失败")
	}
}
