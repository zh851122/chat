package imapi

import "github.com/openimsdk/protocol/sdkws"

// SendSingleMsgReq defines the structure for sending a message to multiple recipients.
type SendSingleMsgReq struct {
	// groupMsg should appoint sendID
	SendID          string                 `json:"sendID"`
	Content         string                 `json:"content" binding:"required"`
	OfflinePushInfo *sdkws.OfflinePushInfo `json:"offlinePushInfo"`
	Ex              string                 `json:"ex"`
}
type SendSingleMsgResp struct{}

type SendMsgReq struct {
	RecvID           string                 `json:"recvID"`
	SendID           string                 `json:"sendID"`
	GroupID          string                 `json:"groupID"`
	SenderPlatformID int32                  `json:"senderPlatformID"`
	Content          map[string]any         `json:"content"`
	ContentType      int32                  `json:"contentType"`
	SessionType      int32                  `json:"sessionType"`
	OfflinePushInfo  *sdkws.OfflinePushInfo `json:"offlinePushInfo,omitempty"`
}

type SendMsgResp struct {
	ServerMsgID string `json:"serverMsgID"`
	ClientMsgID string `json:"clientMsgID"`
	SendTime    int64  `json:"sendTime"`
}
