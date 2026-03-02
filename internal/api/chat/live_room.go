package chat

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
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

	initOnce sync.Once
	initErr  error
	coll     *mongo.Collection
}

func NewLiveRoomAPI(cfg *Config) *LiveRoomAPI {
	return &LiveRoomAPI{
		cfg: cfg,
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
			{"group_id": bson.M{"$regex": req.Keyword, "$options": "i"}},
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
	expiresAt := time.Now().Add(time.Duration(cfg.ExpireSeconds) * time.Second).Unix()
	playRtmp, playFlv, playHls := buildTencentPlayURLs(cfg.PlayDomain, cfg.AppName, room.StreamName, cfg.PlayKey, expiresAt)
	apiresp.GinSuccess(c, &getUrlsResp{
		ExpiresAtUnix: expiresAt,
		PlayRtmpURL:   playRtmp,
		PlayFlvURL:    playFlv,
		PlayHlsURL:    playHls,
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
