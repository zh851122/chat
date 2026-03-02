package admin

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openimsdk/chat/pkg/common/imapi"
	"github.com/openimsdk/chat/pkg/common/mctx"
	"github.com/openimsdk/tools/apiresp"
	"github.com/openimsdk/tools/db/mongoutil"
	"github.com/openimsdk/tools/errs"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type LiveRoomAPI struct {
	cfg *Config
	im  imapi.CallerInterface

	defaultSenderID string

	initOnce sync.Once
	initErr  error
	coll     *mongo.Collection
}

func NewLiveRoomAPI(cfg *Config, im imapi.CallerInterface, defaultSenderID string) *LiveRoomAPI {
	return &LiveRoomAPI{
		cfg:             cfg,
		im:              im,
		defaultSenderID: defaultSenderID,
	}
}

func (o *LiveRoomAPI) init(ctx context.Context) error {
	o.initOnce.Do(func() {
		mgocli, err := mongoutil.NewMongoDB(ctx, o.cfg.Mongo.Build())
		if err != nil {
			o.initErr = err
			return
		}
		o.coll = mgocli.GetDB().Collection("live_room")
		_, err = o.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: "stream_name", Value: 1}},
			Options: options.Index().
				SetName("uniq_stream_name").
				SetUnique(true),
		})
		if err != nil {
			o.initErr = errs.WrapMsg(err, "create live_room index")
			return
		}
	})
	return o.initErr
}

type livePagination struct {
	PageNumber int32 `json:"pageNumber"`
	ShowNumber int32 `json:"showNumber"`
}

func (p *livePagination) GetPageNumber() int32 {
	if p == nil {
		return 0
	}
	return p.PageNumber
}

func (p *livePagination) GetShowNumber() int32 {
	if p == nil {
		return 0
	}
	return p.ShowNumber
}

type liveRoomDoc struct {
	ID         primitive.ObjectID `bson:"_id,omitempty"`
	Name       string             `bson:"name"`
	StreamName string             `bson:"stream_name"`
	GroupID    string             `bson:"group_id,omitempty"`
	Remark     string             `bson:"remark,omitempty"`
	CreateTime time.Time          `bson:"create_time"`
	UpdateTime time.Time          `bson:"update_time"`
}

type liveRoomView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	StreamName string `json:"streamName"`
	GroupID    string `json:"groupID,omitempty"`
	Remark     string `json:"remark"`
	CreateTime int64  `json:"createTime"`
	UpdateTime int64  `json:"updateTime"`
}

func docToView(d liveRoomDoc) liveRoomView {
	return liveRoomView{
		ID:         d.ID.Hex(),
		Name:       d.Name,
		StreamName: d.StreamName,
		GroupID:    d.GroupID,
		Remark:     d.Remark,
		CreateTime: d.CreateTime.UnixMilli(),
		UpdateTime: d.UpdateTime.UnixMilli(),
	}
}

func (o *LiveRoomAPI) GetConfig(c *gin.Context) {
	cfg := o.cfg.Share.TencentLive
	if cfg.ExpireSeconds <= 0 {
		cfg.ExpireSeconds = 3600
	}
	apiresp.GinSuccess(c, gin.H{
		"pushDomain":    cfg.PushDomain,
		"playDomain":    cfg.PlayDomain,
		"appName":       cfg.AppName,
		"expireSeconds": cfg.ExpireSeconds,
	})
}

type createLiveRoomReq struct {
	Name       string `json:"name"`
	StreamName string `json:"streamName"`
	GroupID    string `json:"groupID"`
	Remark     string `json:"remark"`
}

func (o *LiveRoomAPI) ensureRoomGroup(c *gin.Context, req createLiveRoomReq) (string, error) {
	if o.im == nil {
		return "", errs.ErrInternalServer.WrapMsg("im caller is nil")
	}
	ownerUserID := o.defaultSenderID
	if ownerUserID == "" {
		ownerUserID = o.cfg.Share.OpenIM.AdminUserID
	}
	if ownerUserID == "" {
		return "", errs.ErrArgs.WrapMsg("owner user id is empty")
	}
	imToken, err := o.im.ImAdminTokenWithDefaultAdmin(c)
	if err != nil {
		return "", err
	}
	apiCtx := mctx.WithApiToken(c, imToken)

	if req.GroupID != "" {
		groupInfos, err := o.im.FindGroupInfo(apiCtx, []string{req.GroupID})
		if err == nil && len(groupInfos) > 0 {
			return req.GroupID, nil
		}
	}

	notification := req.Remark
	if notification == "" {
		notification = "live room public chat group"
	}
	return o.im.CreateGroup(apiCtx, ownerUserID, req.GroupID, req.Name, notification)
}

func (o *LiveRoomAPI) CreateRoom(c *gin.Context) {
	if err := o.init(c); err != nil {
		apiresp.GinError(c, err)
		return
	}
	var req createLiveRoomReq
	if err := c.BindJSON(&req); err != nil {
		apiresp.GinError(c, errs.ErrArgs.WithDetail(err.Error()).Wrap())
		return
	}
	if req.Name == "" {
		apiresp.GinError(c, errs.ErrArgs.WrapMsg("name is empty"))
		return
	}
	if req.StreamName == "" {
		req.StreamName = primitive.NewObjectID().Hex()
	}
	count, err := o.coll.CountDocuments(c, bson.M{"stream_name": req.StreamName})
	if err != nil {
		apiresp.GinError(c, errs.WrapMsg(err, "count stream name"))
		return
	}
	if count > 0 {
		apiresp.GinError(c, errs.ErrDuplicateKey.WrapMsg("stream name existed"))
		return
	}
	groupID, err := o.ensureRoomGroup(c, req)
	if err != nil {
		apiresp.GinError(c, errs.WrapMsg(err, "ensure room group failed"))
		return
	}
	now := time.Now()
	doc := &liveRoomDoc{
		ID:         primitive.NewObjectID(),
		Name:       req.Name,
		StreamName: req.StreamName,
		GroupID:    groupID,
		Remark:     req.Remark,
		CreateTime: now,
		UpdateTime: now,
	}
	if err := mongoutil.InsertMany(c, o.coll, []*liveRoomDoc{doc}); err != nil {
		apiresp.GinError(c, err)
		return
	}
	apiresp.GinSuccess(c, docToView(*doc))
}

type deleteLiveRoomReq struct {
	IDs []string `json:"ids"`
}

func (o *LiveRoomAPI) DeleteRoom(c *gin.Context) {
	if err := o.init(c); err != nil {
		apiresp.GinError(c, err)
		return
	}
	var req deleteLiveRoomReq
	if err := c.BindJSON(&req); err != nil {
		apiresp.GinError(c, errs.ErrArgs.WithDetail(err.Error()).Wrap())
		return
	}
	if len(req.IDs) == 0 {
		apiresp.GinError(c, errs.ErrArgs.WrapMsg("ids is empty"))
		return
	}
	oids := make([]primitive.ObjectID, 0, len(req.IDs))
	for _, id := range req.IDs {
		oid, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			apiresp.GinError(c, errs.ErrArgs.WrapMsg("invalid id "+err.Error()))
			return
		}
		oids = append(oids, oid)
	}
	_, err := o.coll.DeleteMany(c, bson.M{"_id": bson.M{"$in": oids}})
	if err != nil {
		apiresp.GinError(c, errs.WrapMsg(err, "delete live rooms"))
		return
	}
	apiresp.GinSuccess(c, nil)
}

type listLiveRoomReq struct {
	Keyword    string          `json:"keyword"`
	Pagination *livePagination `json:"pagination"`
}

type listLiveRoomResp struct {
	Total int64          `json:"total"`
	List  []liveRoomView `json:"list"`
}

func (o *LiveRoomAPI) ListRoom(c *gin.Context) {
	if err := o.init(c); err != nil {
		apiresp.GinError(c, err)
		return
	}
	var req listLiveRoomReq
	if err := c.BindJSON(&req); err != nil {
		apiresp.GinError(c, errs.ErrArgs.WithDetail(err.Error()).Wrap())
		return
	}
	filter := bson.M{}
	if req.Keyword != "" {
		filter["$or"] = []bson.M{
			{"name": bson.M{"$regex": req.Keyword, "$options": "i"}},
			{"stream_name": bson.M{"$regex": req.Keyword, "$options": "i"}},
		}
	}
	total, docs, err := mongoutil.FindPage[liveRoomDoc](c, o.coll, filter, req.Pagination, options.Find().SetSort(bson.D{{Key: "create_time", Value: -1}}))
	if err != nil {
		apiresp.GinError(c, err)
		return
	}
	views := make([]liveRoomView, 0, len(docs))
	for _, d := range docs {
		views = append(views, docToView(d))
	}
	apiresp.GinSuccess(c, listLiveRoomResp{Total: total, List: views})
}

type getUrlsReq struct {
	ID            string `json:"id"`
	ExpireSeconds int64  `json:"expireSeconds"`
}

type getUrlsResp struct {
	ExpiresAtUnix int64  `json:"expiresAtUnix"`
	PushURL       string `json:"pushUrl"`
	ObsServer     string `json:"obsServer"`
	ObsStreamKey  string `json:"obsStreamKey"`
	PlayRtmpURL   string `json:"playRtmpUrl"`
	PlayFlvURL    string `json:"playFlvUrl"`
	PlayHlsURL    string `json:"playHlsUrl"`
}

func (o *LiveRoomAPI) GetRoomURLs(c *gin.Context) {
	if err := o.init(c); err != nil {
		apiresp.GinError(c, err)
		return
	}
	var req getUrlsReq
	if err := c.BindJSON(&req); err != nil {
		apiresp.GinError(c, errs.ErrArgs.WithDetail(err.Error()).Wrap())
		return
	}
	oid, err := primitive.ObjectIDFromHex(req.ID)
	if err != nil {
		apiresp.GinError(c, errs.ErrArgs.WrapMsg("invalid id "+err.Error()))
		return
	}
	room, err := mongoutil.FindOne[liveRoomDoc](c, o.coll, bson.M{"_id": oid})
	if err != nil {
		apiresp.GinError(c, err)
		return
	}
	cfg := o.cfg.Share.TencentLive
	if req.ExpireSeconds > 0 {
		cfg.ExpireSeconds = req.ExpireSeconds
	}
	if cfg.ExpireSeconds <= 0 {
		cfg.ExpireSeconds = 3600
	}
	if cfg.AppName == "" {
		cfg.AppName = "live"
	}
	pushURL, obsServer, obsStreamKey, expiresAt := buildTencentPushURL(cfg.PushDomain, cfg.AppName, room.StreamName, cfg.PushKey, cfg.ExpireSeconds)
	playRtmp, playFlv, playHls := buildTencentPlayURLs(cfg.PlayDomain, cfg.AppName, room.StreamName, cfg.PlayKey, expiresAt)
	apiresp.GinSuccess(c, &getUrlsResp{
		ExpiresAtUnix: expiresAt,
		PushURL:       pushURL,
		ObsServer:     obsServer,
		ObsStreamKey:  obsStreamKey,
		PlayRtmpURL:   playRtmp,
		PlayFlvURL:    playFlv,
		PlayHlsURL:    playHls,
	})
}

type mockBarrageReq struct {
	ID           string `json:"id"`
	Content      string `json:"content"`
	Repeat       int    `json:"repeat"`
	IntervalMS   int    `json:"intervalMs"`
	SenderUserID string `json:"senderUserID"`
}

type mockBarrageResp struct {
	GroupID string `json:"groupID"`
	Sent    int    `json:"sent"`
}

func (o *LiveRoomAPI) MockRoomBarrage(c *gin.Context) {
	if err := o.init(c); err != nil {
		apiresp.GinError(c, err)
		return
	}
	if o.im == nil {
		apiresp.GinError(c, errs.ErrInternalServer.WrapMsg("im caller is nil"))
		return
	}
	var req mockBarrageReq
	if err := c.BindJSON(&req); err != nil {
		apiresp.GinError(c, errs.ErrArgs.WithDetail(err.Error()).Wrap())
		return
	}
	if req.Content == "" {
		apiresp.GinError(c, errs.ErrArgs.WrapMsg("content is empty"))
		return
	}
	if req.Repeat <= 0 {
		req.Repeat = 1
	}
	if req.Repeat > 50 {
		req.Repeat = 50
	}
	if req.IntervalMS < 0 {
		req.IntervalMS = 0
	}
	if req.IntervalMS > 5000 {
		req.IntervalMS = 5000
	}
	oid, err := primitive.ObjectIDFromHex(req.ID)
	if err != nil {
		apiresp.GinError(c, errs.ErrArgs.WrapMsg("invalid id "+err.Error()))
		return
	}
	room, err := mongoutil.FindOne[liveRoomDoc](c, o.coll, bson.M{"_id": oid})
	if err != nil {
		apiresp.GinError(c, err)
		return
	}
	groupID := room.GroupID
	if groupID == "" {
		groupID = room.StreamName
	}
	if groupID == "" {
		apiresp.GinError(c, errs.ErrArgs.WrapMsg("group id is empty"))
		return
	}
	senderID := req.SenderUserID
	if senderID == "" {
		senderID = o.defaultSenderID
	}
	if senderID == "" {
		apiresp.GinError(c, errs.ErrArgs.WrapMsg("sender user id is empty"))
		return
	}
	imToken, err := o.im.ImAdminTokenWithDefaultAdmin(c)
	if err != nil {
		apiresp.GinError(c, err)
		return
	}
	ctx := mctx.WithApiToken(c, imToken)
	sent := 0
	for i := 0; i < req.Repeat; i++ {
		content := req.Content
		if req.Repeat > 1 {
			content = fmt.Sprintf("%s %d/%d", req.Content, i+1, req.Repeat)
		}
		if err := o.im.SendGroupTextMsg(ctx, senderID, groupID, content); err != nil {
			apiresp.GinError(c, errs.WrapMsg(err, "send group barrage failed"))
			return
		}
		sent++
		if req.IntervalMS > 0 && i < req.Repeat-1 {
			select {
			case <-ctx.Done():
				apiresp.GinError(c, errs.ErrInternalServer.WrapMsg("request canceled"))
				return
			case <-time.After(time.Duration(req.IntervalMS) * time.Millisecond):
			}
		}
	}
	apiresp.GinSuccess(c, mockBarrageResp{
		GroupID: groupID,
		Sent:    sent,
	})
}

func buildTencentAuthQuery(key string, streamName string, expiresAtUnix int64) string {
	if key == "" || expiresAtUnix <= 0 {
		return ""
	}
	txTime := fmt.Sprintf("%X", expiresAtUnix)
	sum := md5.Sum([]byte(key + streamName + txTime))
	txSecret := hex.EncodeToString(sum[:])
	return fmt.Sprintf("?txSecret=%s&txTime=%s", txSecret, txTime)
}

func buildTencentPlayURLs(playDomain string, appName string, streamName string, playKey string, expiresAtUnix int64) (string, string, string) {
	if playDomain == "" {
		return "", "", ""
	}
	if appName == "" {
		appName = "live"
	}
	query := buildTencentAuthQuery(playKey, streamName, expiresAtUnix)
	rtmp := fmt.Sprintf("rtmp://%s/%s/%s%s", playDomain, appName, streamName, query)
	flv := fmt.Sprintf("https://%s/%s/%s.flv%s", playDomain, appName, streamName, query)
	hls := fmt.Sprintf("https://%s/%s/%s.m3u8%s", playDomain, appName, streamName, query)
	return rtmp, flv, hls
}

func buildTencentPushURL(pushDomain string, appName string, streamName string, pushKey string, expireSeconds int64) (string, string, string, int64) {
	if pushDomain == "" {
		return "", "", "", 0
	}
	if appName == "" {
		appName = "live"
	}
	if expireSeconds <= 0 {
		expireSeconds = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expireSeconds) * time.Second).Unix()
	query := buildTencentAuthQuery(pushKey, streamName, expiresAt)

	pushURL := fmt.Sprintf("rtmp://%s/%s/%s%s", pushDomain, appName, streamName, query)
	obsServer := fmt.Sprintf("rtmp://%s/%s", pushDomain, appName)
	obsStreamKey := fmt.Sprintf("%s%s", streamName, query)
	return pushURL, obsServer, obsStreamKey, expiresAt
}
