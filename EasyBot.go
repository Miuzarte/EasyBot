package EasyBot

import (
	"EasyBot/SimpleLogFormatter"
	"EasyBot/TimeLayout"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/ysmood/gson"
	"golang.org/x/net/websocket"
)

type CQBot struct {
	WsUrl    string          //WebSocket通信地址
	Conn     *websocket.Conn //WebSocket连接
	ConnLost chan struct{}   //连接断开信号

	IsExpectedTermination   bool   //是否为预期中的终止连接
	OnUnexpectedTermination func() //预料之外的断开回调

	SelfID     int   //机器人账号
	SuperUsers []int //超级用户列表

	NickName                    []string  //机器人别称(用于判断IsToMe)
	StartTime                   time.Time //此次上线时间
	IsEnableOnlineNotification  bool      //是否启用上线通知
	IsEnableOfflineNotification bool      //是否启用下线通知
	RetryCount                  int       //连接重试次数
	IsHeartbeatChecking         bool      //是否存在心跳监听协程
	IsHeartbeatOK               bool      //是否正常接收到心跳包
	HeartbeatCount              int       //接收心跳包计数
	HeartbeatLostCount          int       //心跳包丢失计数
	HeartbeatInterval           int       //心跳包间隔(ms)
	//HeartbeatWaitGroup          sync.WaitGroup        //心跳包等待
	Heartbeat      chan struct{}         //心跳包接收传入通道
	Wg             sync.WaitGroup        //等待
	ApiCallTimeOut time.Duration         //调用超时时间
	ApiCallNotice  chan struct{}         //Api调用响应通知
	ApiCallResp    map[string]*CQApiResp //Api调用响应 echo:*CQApiResp
	// ACRMutex                    sync.Mutex            //Api调用响应锁

	BlackList *blackList //屏蔽列表 不执行由其触发的消息回调

	NickNameTable       map[int]string             //QQ昵称 UserID:NickName
	NNTMutex            sync.Mutex                 //QQ昵称锁
	CardNameTable       map[int]map[int]string     //群名片 UserID:GroupID:CardName
	CNTMutex            sync.Mutex                 //群名片锁
	MessageTablePrivate map[int]map[int]*CQMessage //私聊消息缓存 UserID:MessageID:*CQMessage
	MTPMutex            sync.Mutex                 //私聊消息表锁
	MessageTableGroup   map[int]map[int]*CQMessage //群聊消息缓存 GroupID:MessageID:*CQMessage
	MTGMutex            sync.Mutex                 //群聊消息表锁
	Log2SU              *log2SU                    //向管理员发送通知
	Utils               *utilsFunc                 //小工具

	On struct { //回调
		Recv    func(*CQRecv)    //下发
		ApiResp func(*CQApiResp) //API响应
		Event   func(*CQEvent)   //事件

		Message        func(*CQMessage) //消息
		MessagePrivate func(*CQMessage) //私聊消息
		MessageGroup   func(*CQMessage) //群消息

		Notice       func(*CQNotice)             //通知
		FriendRecall func(*CQNoticeFriendRecall) //私聊消息撤回
		GroupRecall  func(*CQNoticeGroupRecall)  //群消息撤回
		GroupCard    func(*CQNoticeGroupCard)    //群名片变更
		GroupUpload  func(*CQNoticeGroupUpload)  //离线文件上传
		OfflineFile  func(*CQNoticeOfflineFile)  //离线文件上传
		Notify       func(*CQNoticeNotify)       //系统通知
		Poke         func(*CQNoticeNotifyPoke)   //戳一戳
		//......

		Request       func(*CQRequest)       //请求
		RequestFriend func(*CQRequestFriend) //加好友请求
		RequestGroup  func(*CQRequestGroup)  //加群请求/邀请

		MetaEvent func(*CQMetaEvent)          //元事件
		Heartbeat func(*CQMetaEventHeartbeat) //心跳包
		Lifecycle func(*CQMetaEventLifecycle) //生命周期
	}

	log      *logrus.Logger //内部日志输出
	logLevel logrus.Level   //内部日志等级
}

type blackList struct {
	private []int
	group   []int
}

type CQPost struct {
	Bot *CQBot

	Raw map[string]any
}
type CQRecv struct {
	Bot *CQBot

	Raw []byte
}

type CQForwardMsg []CQForwardNode //可以直接用Send(Private/Group)ForwardMsg()发送的
type CQForwardNode map[string]any //单个消息节点, 需要用NewForwardMsg() / AppendForwardMsg()包装成CQForwardMsg才能发送

type CQCardMsg struct {
	App string `json:"app"`
}

// CQEvent 事件
type CQEvent struct {
	Bot  *CQBot
	Recv *CQRecv

	Time   int `json:"time"`
	SelfID int `json:"self_id"`
	//"message"消息, "message_sent"消息发送,
	//"request"请求, "notice"通知, "meta_event"元事件
	PostType string `json:"post_type"`
}

// CQMessage 消息
type CQMessage struct {
	Bot   *CQBot
	Event *CQEvent

	Time int `json:"time"` //get_msg用的

	//////// vvvv    GENERAL    vvvv

	//"private"私聊消息, "group"群消息
	MessageType string `json:"message_type"`

	//"friend"好友, "normal"群聊,
	//"anonymous"匿名, "group_self"群中自身发送,
	//"group"群临时会话, "notice"系统提示
	SubType string `json:"sub_type"`

	//消息ID
	MessageID int `json:"message_id"`
	//发送者QQ
	UserID int `json:"user_id"`

	//取决于上报格式 string OR []json
	Message any `json:"message"`
	//纯文本(CQ码) /get_msg获取时没有
	RawMessage string `json:"raw_message"`

	//表示消息发送者的信息
	Sender struct {
		////////general

		Age      int    `json:"age"` //(恒)0
		Sex      string `json:"sex"` //(恒)"unknown"
		UserID   int    `json:"user_id"`
		NickName string `json:"nickname"` //QQ昵称
		////////--------

		////////temp

		GroupID int `json:"group_id"` //临时会话来源
		////////--------

		////////group

		Area  string `json:"area"`  //(恒)""
		Level string `json:"level"` //(恒)""

		Role         string `json:"role"`  //"member", "admin", "owner"
		CardName     string `json:"card"`  //群名片
		SpecialTitle string `json:"title"` //专属头衔
		////////--------
	} `json:"sender"`

	Font int `json:"font"` //(恒)0

	////////----------------

	////////private

	//接收者QQ号
	TargetID int `json:"target_id"`
	//临时会话来源
	TempSource int `json:"temp_source"`
	////////----------------

	////////group

	//群号
	GroupID int `json:"group_id"`
	//(为什么文档里没有)
	MessageSeq int `json:"message_seq"`
	//匿名信息, 如果不是匿名消息则为 null
	Anonymous struct {
		ID   int    `json:"id"`   //匿名用户ID
		Name string `json:"name"` //匿名用户名称
		Flag string `json:"flag"` //匿名用户flag, 在调用禁言API时需要传入
	} `json:"anonymous"`
	////////----------------

	//附加数据
	Extra struct { //NothingBot附加数据
		Recalled         bool   //是否被撤回
		OperatorID       int    //撤回者ID
		TimeFormat       string //格式化的时间
		MessageWithReply string //带回复内容的消息
		AtWho            []int  //@的人
	}
}

/*
CQNotice 通知

"group_upload"群文件上传,

"group_admin"群管理员变更,

"group_decrease"群成员减少,

"group_increase"群成员增加,

"group_ban"群成员禁言,

"friend_add"好友添加,

"group_recall"群消息撤回,

"friend_recall"好友消息撤回,

"group_card"群名片变更,

"offline_file"离线文件上传,

"client_status"客户端状态变更,

"essence"精华消息,

"notify"系统通知
*/
type CQNotice struct {
	Bot   *CQBot
	Event *CQEvent

	//////// vvvv    GENERAL    vvvv

	NoticeType string `json:"notice_type"` //"notify"...

	//////// ^^^^----------------^^^^
}
type CQNoticeFriendRecall struct { //好友消息撤回
	Notice *CQNotice

	UserID    int `json:"user_id"`
	MessageID int `json:"message_id"`
}
type CQNoticeGroupRecall struct { //群消息撤回
	Notice *CQNotice

	GroupID    int `json:"group_id"`
	OperatorID int `json:"operator_id"`
	UserID     int `json:"user_id"`
	MessageID  int `json:"message_id"`
}
type CQNoticeGroupCard struct { //群名片变更
	Notice *CQNotice

	GroupID int    `json:"group_id"`
	UserID  int    `json:"user_id"`
	CardOld string `json:"card_old"`
	CardNew string `json:"card_new"`
}
type CQNoticeGroupUpload struct { //群文件上传
	Notice *CQNotice

	GroupID int `json:"group_id"`
	UserID  int `json:"user_id"`
	File    struct {
		Name  string `json:"name"`
		Size  int    `json:"size"` //(Byte)
		Url   string `json:"url"`
		Busid int    `json:"busid"`
	} `json:"file"`
}
type CQNoticeOfflineFile struct { //离线文件上传
	Notice *CQNotice

	UserID int `json:"user_id"`
	File   struct {
		Name string `json:"name"`
		Size int    `json:"size"` //(Byte)
		Url  string `json:"url"`
	} `json:"file"`
}
type CQNoticeNotify struct { //系统通知
	Notice *CQNotice

	//////// vvvv    GENERAL    vvvv

	//"poke"戳一戳,
	//"lucky_king"群红包运气王,
	//"honor"群成员荣誉变更,
	//"title"群成员头衔变更
	SubType string `json:"sub_type"`

	//////// ^^^^----------------^^^^
}
type CQNoticeNotifyPoke struct { //系统通知_戳一戳
	Notify *CQNoticeNotify

	UserID   int `json:"user_id"`
	TargetID int `json:"target_id"`
	// SenderID int `json:"sender_id"` //go-cqhttp
	OperatorID int `json:"operator_id"`

	//for group poke
	GroupID int `json:"group_id"`
}

/*
CQRequest 请求

"friend"加好友请求,

"request"加群请求/邀请
*/
type CQRequest struct {
	Bot   *CQBot
	Event *CQEvent

	//////// vvvv    GENERAL    vvvv

	RequestType string `json:"request_type"`

	//////// ^^^^----------------^^^^
}
type CQRequestFriend struct { //加好友请求
	Request *CQRequest

	//发送请求的QQ号
	UserID int `json:"user_id"`
	//验证消息
	Comment string `json:"comment"`
	//请求flag, 在调用处理请求的API时需要传入
	Flag string `json:"flag"`
}
type CQRequestGroup struct { //加群请求/邀请
	Request *CQRequest

	//请求子类型,
	//"add"加群请求,
	//"invivte"被邀请加群
	SubType string `json:""`

	//群号
	GroupID int `json:""`
	//发送请求的QQ号
	UserID int `json:"user_id"`
	//验证消息
	Comment string `json:"comment"`
	//请求flag, 在调用处理请求的API时需要传入
	Flag string `json:"flag"`
}

/*
CQMetaEvent 元事件

"heartbeat"心跳包,

"lifecycle"生命周期
*/
type CQMetaEvent struct {
	Bot   *CQBot
	Event *CQEvent

	//////// vvvv    GENERAL    vvvv

	MetaEventType string `json:"meta_event_type"`

	//////// ^^^^----------------^^^^
}
type CQMetaEventHeartbeat struct { //心跳包
	MetaEvent *CQMetaEvent

	//距离上一次心跳包的时间(ms)
	//shamrock 15000
	Interval int `json:"interval"`
	//机器人账号
	SelfID int `json:"self_id"`
	//状态
	Status struct {
		Online   bool   `json:"online"`
		Good     bool   `json:"good"`
		QQStatus string `json:"qq.status"`
		Self     struct {
			Platform string `json:"platform"`
			UserID   int    `json:"user_id"`
		} `json:"self"`
	} `json:"status"`
	//子类型(恒)"connect"
	SubType string `json:"sub_type"`
}
type CQMetaEventLifecycle struct { //生命周期
	MetaEvent *CQMetaEvent

	//距离上一次心跳包的时间(ms)
	//shamrock 15000
	Interval int `json:"interval"`
	//机器人账号
	SelfID int `json:"self_id"`
	//状态
	Status struct {
		Online   bool   `json:"online"`
		Good     bool   `json:"good"`
		QQStatus string `json:"qq.status"`
		Self     struct {
			Platform string `json:"platform"`
			UserID   int    `json:"user_id"`
		} `json:"self"`
	} `json:"status"`

	//上报方式(恒)2 _gocq
	PostMethod int `json:"_post_method"`
	//子类型(恒)"connect"
	SubType string `json:"sub_type"`
}

// CQApiResp API响应
type CQApiResp struct {
	Bot  *CQBot
	Recv *CQRecv

	//规定每次上报都要有echo
	Echo    string         `json:"echo"`
	Status  any            `json:"status"` //响应时是string, 心跳时是map[string]any
	RetCode int            `json:"retcode"`
	Msg     string         `json:"msg"`
	Wording string         `json:"wording"`
	Data    map[string]any `json:"data"`
	raw     []byte
}

var (
	e = struct {
		general        error
		noEcho         error
		unknownMsgType error
		noSU           error
		noConnect      error
		needEcho       error
		wrongType      error
	}{
		general:        errors.New("OCCURRED ERROR"),
		noEcho:         errors.New("CANT GET ECHO"),
		unknownMsgType: errors.New("UNKNOWN MESSAGE TYPE"),
		noSU:           errors.New("AT LEAST ONE SU IS REQUIRED"),
		noConnect:      errors.New("DID NOT CONNECT TO GO-CQHTTP"),
		needEcho:       errors.New("API CALLING MUST BE WITH ECHO"),
		wrongType:      errors.New("WRONG INPUT TYPE"),
	}
)

var (
	unescape = strings.NewReplacer(
		//反转义还原CQ码
		"&amp;", "&", "&#44;", ",", "&#91;", "[", "&#93;", "]",
	)
)

type log2SU struct {
	bot *CQBot
}

func (l *log2SU) Trace(msg ...any) (err error) {
	return l.bot.SendPrivateMsgs(l.bot.SuperUsers, fmt.Sprint("[Trace] ", fmt.Sprint(msg...)))
}
func (l *log2SU) Debug(msg ...any) (err error) {
	return l.bot.SendPrivateMsgs(l.bot.SuperUsers, fmt.Sprint("[Debug] ", fmt.Sprint(msg...)))
}
func (l *log2SU) Info(msg ...any) (err error) {
	return l.bot.SendPrivateMsgs(l.bot.SuperUsers, fmt.Sprint("[Info] ", fmt.Sprint(msg...)))
}
func (l *log2SU) Warn(msg ...any) (err error) {
	return l.bot.SendPrivateMsgs(l.bot.SuperUsers, fmt.Sprint("[Warn] ", fmt.Sprint(msg...)))
}
func (l *log2SU) Error(msg ...any) (err error) {
	return l.bot.SendPrivateMsgs(l.bot.SuperUsers, fmt.Sprint("[Error] ", fmt.Sprint(msg...)))
}
func (l *log2SU) Fatal(msg ...any) (err error) {
	return l.bot.SendPrivateMsgs(l.bot.SuperUsers, fmt.Sprint("[Fatal] ", fmt.Sprint(msg...)))
}
func (l *log2SU) Panic(msg ...any) (err error) {
	return l.bot.SendPrivateMsgs(l.bot.SuperUsers, fmt.Sprint("[Panic] ", fmt.Sprint(msg...)))
}

// New 新建并初始化
func New() *CQBot {
	bot := &CQBot{
		BlackList: &blackList{
			private: []int{},
			group:   []int{},
		},
	}
	bot.logLevel = logrus.InfoLevel
	bot.log = logrus.New()
	bot.log.SetLevel(bot.logLevel) //默认显示内部日志
	bot.log.SetFormatter(&SimpleLogFormatter.LogFormat{})
	bot.Log2SU = &log2SU{
		bot: bot,
	}
	bot.Heartbeat = make(chan struct{})
	bot.ConnLost = make(chan struct{})
	bot.ApiCallTimeOut = time.Second * 60
	bot.ApiCallNotice = make(chan struct{})
	bot.ApiCallResp = make(map[string]*CQApiResp)
	bot.MessageTablePrivate = make(map[int]map[int]*CQMessage)
	bot.MessageTableGroup = make(map[int]map[int]*CQMessage)
	bot.NickNameTable = make(map[int]string)
	bot.CardNameTable = make(map[int]map[int]string)
	bot.Utils = &utilsFunc{
		bot:    bot,
		Format: &formater{},
	}
	bot.Utils.Format.utils = bot.Utils
	return bot
}

// SetWsUrl 设置WebSocket链接
func (bot *CQBot) SetWsUrl(url string) *CQBot {
	matches := regexp.MustCompile(`ws://`).FindAllStringSubmatch(url, -1)
	if len(matches) == 0 {
		url = "ws://" + url
	}
	bot.WsUrl = url
	return bot
}

// GetSelfId 获取自身Q号_Shamrock
func (bot *CQBot) GetSelfId() (selfID int) {
	return bot.SelfID
}

// GetSelfIdGocq 获取自身Q号_gocq
func (bot *CQBot) GetSelfIdGocq() (selfID int) {
	if bot.SelfID != 0 {
		return bot.SelfID
	}
	bot.log.Debug("[EasyBot] bot.selfID 为 0, 尝试通过 get_login_info 获取selfID")
	selfID, _, err := bot.GetLoginInfo()
	if err != nil {
		bot.log.Error("[EasyBot] GetSelfId().GetLoginInfo()调用失败, err: ", err)
	}
	if !bot.IsHeartbeatOK {
		bot.log.Fatal("[EasyBot] 试图在未连接go-cqhttp时调用bot.GetLoginInfo()")
	}
	return
}

// AddSU 添加超级用户
func (bot *CQBot) AddSU(userIds ...int) *CQBot {
	for _, userId := range userIds {
		if userId != 0 {
			bot.SuperUsers = append(bot.SuperUsers, userId)
		}
	}
	return bot
}

// RmSU 移除超级用户
func (bot *CQBot) RmSU(userIds ...int) *CQBot {
	for _, userId := range userIds {
		if userId != 0 {
			deleteValueInSlice[int](bot.SuperUsers, userId)
		}
	}
	return bot
}

// GetSU 获取超级用户列表
func (bot *CQBot) GetSU() []int {
	return bot.SuperUsers
}

// AddNickName 添加机器人别称(用于判断IsToMe)
func (bot *CQBot) AddNickName(names ...string) *CQBot {
	for _, name := range names {
		if name != "" { //要判断非空
			bot.NickName = append(bot.NickName, name)
		}
	}
	return bot
}

// RmNickName 移除机器人别称
func (bot *CQBot) RmNickName(names ...string) *CQBot {
	for _, name := range names {
		if name != "" {
			deleteValueInSlice[string](bot.NickName, name)
		}
	}
	return bot
}

// GetBotNickName 获取机器人别称列表
func (bot *CQBot) GetBotNickName() []string {
	return bot.NickName
}

// DisableLog 禁用Warn及以下等级的日志输出
func (bot *CQBot) DisableLog() {
	bot.SetLogLevel(logrus.ErrorLevel)
}

// EnableLog 启用日志输出
func (bot *CQBot) EnableLog() {
	bot.SetLogLevel(bot.logLevel)
}

// SetLogLevel 设置日志等级
func (bot *CQBot) SetLogLevel(level logrus.Level) *CQBot {
	bot.logLevel = level
	bot.log.SetLevel(bot.logLevel)
	return bot
}

// EnableOnlineNotification 设置上线通知
func (bot *CQBot) EnableOnlineNotification(enable bool) *CQBot {
	bot.IsEnableOnlineNotification = enable
	return bot
}

// EnableOfflineNotification 设置下线通知
func (bot *CQBot) EnableOfflineNotification(enable bool) *CQBot {
	bot.IsEnableOfflineNotification = enable
	return bot
}

// AddPrivateBan 添加私聊屏蔽
func (bot *CQBot) AddPrivateBan(userIds ...int) *CQBot {
	for _, userId := range userIds {
		if userId != 0 {
			bot.log.Info("[EasyBot] 向私聊屏蔽列表中加入了 ", userId)
			bot.BlackList.private = append(bot.BlackList.private, userId)
		}
	}
	return bot
}

// RmPrivateBan 移除私聊屏蔽, 输入0时清空列表
func (bot *CQBot) RmPrivateBan(userIds ...int) *CQBot {
	for _, userId := range userIds {
		if userId == 0 {
			bot.log.Info("[EasyBot] 清空了私聊屏蔽列表")
			bot.BlackList.private = *new([]int)
			return bot
		}
		bot.log.Info("[EasyBot] 从私聊屏蔽列表中移除了 ", userId)
		deleteValueInSlice[int](bot.BlackList.private, userId)
	}
	return bot
}

// GetPrivateBan 获取私聊屏蔽列表
func (bot *CQBot) GetPrivateBan() []int {
	return bot.BlackList.private
}

// AddGroupBan 添加群聊屏蔽
func (bot *CQBot) AddGroupBan(groupIds ...int) *CQBot {
	for _, groupId := range groupIds {
		if groupId != 0 {
			bot.log.Info("[EasyBot] 向群聊屏蔽列表中加入了 ", groupId)
			bot.BlackList.group = append(bot.BlackList.group, groupId)
		}
	}
	return bot
}

// RmGroupBan 移除群聊屏蔽, 输入0时清空列表
func (bot *CQBot) RmGroupBan(groupIds ...int) *CQBot {
	for _, groupId := range groupIds {
		if groupId == 0 {
			bot.log.Info("[EasyBot] 清空了群聊屏蔽列表")
			bot.BlackList.group = *new([]int)
			return bot
		}
		bot.log.Info("[EasyBot] 从群聊屏蔽列表中移除了 ", groupId)
		deleteValueInSlice[int](bot.BlackList.group, groupId)
	}
	return bot
}

// GetGroupBan 获取群聊屏蔽列表
func (bot *CQBot) GetGroupBan() []int {
	return bot.BlackList.group
}

// GetNickName 获取QQ昵称
func (bot *CQBot) GetNickName(userId int) string {
	bot.NNTMutex.Lock()
	defer bot.NNTMutex.Unlock()
	return bot.NickNameTable[userId]
}

// GetCardName 获取群名片, 空时返回QQ昵称
func (bot *CQBot) GetCardName(groupId, userId int) string {
	bot.CNTMutex.Lock()
	defer bot.CNTMutex.Unlock()
	if cardName := bot.CardNameTable[userId][groupId]; cardName != "" {
		return cardName
	}
	return bot.GetNickName(userId)
}

/*
OnTerminateUnexpectedly 预料之外的断开, 触发的前提是收到了第一个心跳包

e.g.:

	func() {
		bot.Connect(true)
	}

	func() {
		panic(errors.New("Unexpected Termination"))
	}
*/
func (bot *CQBot) OnTerminateUnexpectedly(f func()) *CQBot {
	bot.OnUnexpectedTermination = f
	return bot
}

// OnRecv 数据下发
func (bot *CQBot) OnRecv(f func(*CQRecv)) *CQBot {
	bot.On.Recv = f
	return bot
}

// OnApiResp api调用响应
func (bot *CQBot) OnApiResp(f func(*CQApiResp)) *CQBot {
	bot.On.ApiResp = f
	return bot
}

// OnEvent 事件下发
func (bot *CQBot) OnEvent(f func(*CQEvent)) *CQBot {
	bot.On.Event = f
	return bot
}

// OnMessage 消息接收
func (bot *CQBot) OnMessage(f func(*CQMessage)) *CQBot {
	bot.On.Message = f
	return bot
}

// OnMessagePrivate 私聊消息接收
//
// 更推荐直接在OnMessage中判断*CQMessage.MessageType
func (bot *CQBot) OnMessagePrivate(f func(*CQMessage)) *CQBot {
	bot.On.MessagePrivate = f
	return bot
}

// OnMessageGroup 群聊消息接收
//
// 更推荐直接在OnMessage中判断*CQMessage.MessageType
func (bot *CQBot) OnMessageGroup(f func(*CQMessage)) *CQBot {
	bot.On.MessageGroup = f
	return bot
}

// OnNotice 通知下发
func (bot *CQBot) OnNotice(f func(*CQNotice)) *CQBot {
	bot.On.Notice = f
	return bot
}

// OnFriendRecall 好友消息撤回
func (bot *CQBot) OnFriendRecall(f func(*CQNoticeFriendRecall)) *CQBot {
	bot.On.FriendRecall = f
	return bot
}

// OnGroupRecall 群聊消息撤回
func (bot *CQBot) OnGroupRecall(f func(*CQNoticeGroupRecall)) *CQBot {
	bot.On.GroupRecall = f
	return bot
}

// OnGroupCard 群名片变更
func (bot *CQBot) OnGroupCard(f func(*CQNoticeGroupCard)) *CQBot {
	bot.On.GroupCard = f
	return bot
}

// OnGroupUpload 群文件上传
func (bot *CQBot) OnGroupUpload(f func(*CQNoticeGroupUpload)) *CQBot {
	bot.On.GroupUpload = f
	return bot
}

// OnOfflineFile 离线文件上传
func (bot *CQBot) OnOfflineFile(f func(*CQNoticeOfflineFile)) *CQBot {
	bot.On.OfflineFile = f
	return bot
}

// OnNotify 系统通知下发
func (bot *CQBot) OnNotify(f func(*CQNoticeNotify)) *CQBot {
	bot.On.Notify = f
	return bot
}

// OnPoke 戳一戳
func (bot *CQBot) OnPoke(f func(*CQNoticeNotifyPoke)) *CQBot {
	bot.On.Poke = f
	return bot
}

// OnRequest 请求
func (bot *CQBot) OnRequest(f func(*CQRequest)) *CQBot {
	bot.On.Request = f
	return bot
}

// OnRequestFriend 加好友请求
func (bot *CQBot) OnRequestFriend(f func(*CQRequestFriend)) *CQBot {
	bot.On.RequestFriend = f
	return bot
}

// OnRequestGroup 加群请求/邀请
func (bot *CQBot) OnRequestGroup(f func(*CQRequestGroup)) *CQBot {
	bot.On.RequestGroup = f
	return bot
}

// OnMetaEvent 元事件
func (bot *CQBot) OnMetaEvent(f func(*CQMetaEvent)) *CQBot {
	bot.On.MetaEvent = f
	return bot
}

// OnHeatbeat 心跳
func (bot *CQBot) OnHeatbeat(f func(*CQMetaEventHeartbeat)) *CQBot {
	bot.On.Heartbeat = f
	return bot
}

// OnLifecycle 生命周期
func (bot *CQBot) OnLifecycle(f func(*CQMetaEventLifecycle)) *CQBot {
	bot.On.Lifecycle = f
	return bot
}

// GetRunningTime 返回建立连接到目前经过的时间
func (bot *CQBot) GetRunningTime() time.Duration {
	return time.Since(bot.StartTime)
}

// Disconnect 断开CQ连接
func (bot *CQBot) Disconnect() {
	if bot.IsEnableOfflineNotification {
		err := bot.Log2SU.Info("[EasyBot] 已下线")
		if err != nil {
			bot.log.Error("[EasyBot] 向超级用户发送消息失败: ", err)
		}
	}
	bot.IsExpectedTermination = true
	if bot.Conn != nil {
		err := bot.Conn.Close()
		if err != nil {
			bot.log.Error("[EasyBot] ws连接断开失败: ", err)
		}
	}
}

// Connect 与CQ建立连接
//
// autoRetry 为 true 时会自动尝试重连 (每5s)
//
// 不传入 retryCount 或 retryCount[0] <= 0 时视为无限重试
//
// 传入多个 retryCount 只认第一个
func (bot *CQBot) Connect(autoRetry bool, retryCount ...int) (err error) {
	if bot.WsUrl == "" {
		bot.log.Fatal("EMPTY WEBSOCKET URL")
	}
	if len(bot.SuperUsers) == 0 {
		return e.noSU
	}
	bot.IsExpectedTermination = false
	isInfinite := func() bool {
		if len(retryCount) > 0 {
			return retryCount[0] == 0
		}
		return len(retryCount) == 0
	}()

retryLoop:
	c, err := websocket.Dial(bot.WsUrl, "", "http://127.0.0.1")
	if err != nil {
		bot.log.Error("[EasyBot] 建立ws连接失败, err: ", err)

		if autoRetry {

			if isInfinite {
				bot.log.Warn("[EasyBot] 将在 5 秒后重试")
				time.Sleep(time.Second * 5)
				goto retryLoop
			}

			if retryCount[0]--; retryCount[0] >= 0 {
				bot.log.Warn("[EasyBot] 将在 5 秒后重试 (", retryCount[0], " )")
				time.Sleep(time.Second * 5)
				goto retryLoop
			}

		}

		return
	}

	bot.log.Info("[EasyBot] 建立ws连接成功")
	bot.StartTime = time.Now()
	bot.IsHeartbeatOK = true
	bot.Conn = c
	if bot.IsEnableOnlineNotification {
		go func() {
			err := bot.Log2SU.Info("[EasyBot] 已上线")
			if err != nil {
				bot.log.Error("[EasyBot] 向超级用户发送消息失败: ", err)
			}
		}()
	}
	go bot.recvLoop()
	// bot.initSelfInfo() //RIP go-cqhttp
	return
}

func (bot *CQBot) initSelfInfo() {
	callTime := time.Now()
	selfID, selfNickName, err := bot.GetLoginInfo()
	usedTime := time.Since(callTime)
	bot.log.Debug("[EasyBot] 初始化用时: ", usedTime)
	if err != nil {
		bot.log.Fatal("[EasyBot] 初始化账号信息失败, err: ", err)
	}
	bot.log.Info("[EasyBot] 获取账号信息: ", selfNickName, "(", selfID, ")")
	bot.SelfID = selfID
	bot.AddNickName(selfNickName) //用来识别假at
	bot.log.Info("initSelfInfo return")
}

func (bot *CQBot) recvLoop() {
	defer func() {
		close(bot.ConnLost)
		bot.ConnLost = make(chan struct{})
	}()
	for {
		dataReceived := &CQRecv{
			Bot: bot,
		}
		err := websocket.Message.Receive(bot.Conn, &dataReceived.Raw)
		if !bot.IsHeartbeatOK {
			bot.log.Error("[EasyBot] ws连接意外终止 !IsHeartbeatOK")
			return
		}
		if err == io.EOF {
			if !bot.IsExpectedTermination {
				bot.log.Error("[EasyBot] ws连接意外终止 err == io.EOF")
			}
			return
		}
		if err != nil {
			if !bot.IsExpectedTermination {
				bot.log.Error("[EasyBot] ws连接出错 err: ", err)
			}
			return
		}

		//回调vvvvvvvv
		if bot.On.Recv != nil {
			go bot.On.Recv(dataReceived)
		}
		go bot.handleRecv(dataReceived)
	}
}

// PostData 上报数据
func (bot *CQBot) PostData(pData *CQPost) error {
	if bot.IsHeartbeatOK {
		bData, err := json.Marshal(pData.Raw)
		if err != nil {
			bot.log.Warn(
				"[EasyBot] 序列化出错(json.Marshal(pData.Raw)), err: ", err,
				"\n    resp.Data: ", pData.Raw,
				"\n    Marshal by gson: ", gson.New(pData.Raw).JSON("", ""),
			)
			return err
		}
		bot.log.Trace("[EasyBot] rawPost: ", string(bData))
		go func() {
			_, err := bot.Conn.Write(bData)
			if err != nil {
				bot.log.Error("[EasyBot] 向ws连接发送数据失败: ", err)
			}
		}()
		return nil
	} else {
		bot.log.Error("[EasyBot] 未连接到go-cqhttp!")
		return e.noConnect
	}
}

// 下发处理
func (bot *CQBot) handleRecv(recv *CQRecv) {
	bot.log.Trace("[EasyBot] rawRecv: ", string(recv.Raw))
	var err error

	apiResp := &CQApiResp{
		Bot:  bot,
		Recv: recv,
	}

	err = json.Unmarshal(recv.Raw, apiResp)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 反序列化出错(CQApiResp), 跳过处理, err: ", err,
			"\n    recv.Raw: ", string(recv.Raw),
			"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
		)
		return
	}

	if apiResp.Echo != "" { //规定Api调用必须有echo, 非空即为调用了Api
		apiResp.raw = recv.Raw
		bot.ApiCallResp[apiResp.Echo] = apiResp
		bot.log.Debug("[EasyBot] echo: ", apiResp.Echo)
		close(bot.ApiCallNotice) //通知收到了新的Api调用响应
		bot.ApiCallNotice = make(chan struct{})

		//回调vvvvvvvv
		if bot.On.ApiResp != nil {
			bot.log.Trace("[EasyBot] 执行回调: bot.On.ApiResp")
			go bot.On.ApiResp(apiResp)
		}
		return
	}

	event := &CQEvent{
		Bot:  bot,
		Recv: recv,
	}

	err = json.Unmarshal(recv.Raw, event)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 反序列化出错(Event), 跳过处理, err: ", err,
			"\n    recv.Raw: ", string(recv.Raw),
			"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
		)
		return
	}

	//回调vvvvvvvv
	if bot.On.Event != nil {
		go bot.On.Event(event)
	}

	switch event.PostType {
	case "message":
		msg := &CQMessage{
			Bot:   bot,
			Event: event,
		}

		err = json.Unmarshal(recv.Raw, msg)
		if err != nil {
			bot.log.Warn(
				"[EasyBot] 反序列化出错(Event.Message), 跳过处理, err: ", err,
				"\n    recv.Raw: ", string(recv.Raw),
				"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
			)
			return
		}

		isBanned := func() bool {
			return bot.isBannedPrivate(msg.UserID) || bot.isBannedGroup(msg.GroupID)
		}()

		if msg.UserID != bot.SelfID {
			ban := func() string {
				if isBanned {
					return "(Filtered)"
				}
				return ""
			}()
			switch msg.MessageType {
			case "private":
				bot.log.Info(
					"[EasyBot] ", ban, "收到 ",
					msg.Sender.NickName, "(", msg.UserID, ") 的消息(",
					msg.MessageID, "): ", msg.RawMessage,
				)
			case "group":
				bot.log.Info(
					"[EasyBot] ", ban, "在 ", msg.GroupID, " 收到 ",
					msg.Sender.CardName, "(", msg.Sender.NickName, " ", msg.UserID, ") 的群聊消息(",
					msg.MessageID, "): ", msg.RawMessage,
				)
			}
		} else {
			bot.log.Info("[EasyBot] 收到机器人账号发送的消息(", msg.MessageID, "): ", msg.RawMessage)
		}

		go bot.saveMsg(msg)

		//回调vvvvvvvv
		if !isBanned {
			if bot.On.Message != nil {
				go bot.On.Message(msg)
			}
			switch msg.MessageType {
			case "private":
				if bot.On.MessagePrivate != nil {
					go bot.On.MessagePrivate(msg)
				}
			case "group":
				if bot.On.MessagePrivate != nil {
					go bot.On.MessageGroup(msg)
				}
			}
		}

	case "message_sent":
		//......
	case "request":
		request := &CQRequest{
			Bot:   bot,
			Event: event,
		}

		err = json.Unmarshal(recv.Raw, request)
		if err != nil {
			bot.log.Warn(
				"[EasyBot] 反序列化出错(Event.Request), 跳过处理, err: ", err,
				"\n    recv.Raw: ", string(recv.Raw),
				"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
			)
			return
		}

		//回调vvvvvvvv
		if bot.On.Request != nil {
			go bot.On.Request(request)
		}

		switch request.RequestType {
		case "friend":
			friend := &CQRequestFriend{
				Request: request,
			}

			err = json.Unmarshal(recv.Raw, friend)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Request.Friend), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			bot.log.Info("[EasyBot] 收到 ", friend.UserID, " 的好友申请")

			//回调vvvvvvvv
			if bot.On.RequestFriend != nil {
				go bot.On.RequestFriend(friend)
			}

		case "group":
			group := &CQRequestGroup{
				Request: request,
			}

			err = json.Unmarshal(recv.Raw, group)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Request.Group), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			switch group.SubType {
			case "add":
				bot.log.Info("[EasyBot] 群 ", group.GroupID, " 收到 ", group.UserID, " 的加群申请, 验证消息: ", group.Comment)
			case "invite":
				bot.log.Info(
					"[EasyBot] 好友 ", group.UserID, " 邀请机器人加入群 ", group.GroupID, ", 验证消息(应为空?): ",
					group.Comment,
				)
			}

			//回调vvvvvvvv
			if bot.On.RequestGroup != nil {
				go bot.On.RequestGroup(group)
			}

		}
	case "notice":
		notice := &CQNotice{
			Bot:   bot,
			Event: event,
		}

		err = json.Unmarshal(recv.Raw, notice)
		if err != nil {
			bot.log.Warn(
				"[EasyBot] 反序列化出错(Event.Notice), 跳过处理, err: ", err,
				"\n    recv.Raw: ", string(recv.Raw),
				"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
			)
			return
		}

		//回调vvvvvvvv
		if bot.On.Notice != nil {
			go bot.On.Notice(notice)
		}

		switch notice.NoticeType {
		case "group_recall": //群消息撤回
			groupRecall := &CQNoticeGroupRecall{
				Notice: notice,
			}

			err = json.Unmarshal(recv.Raw, groupRecall)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Notice.GroupRecall), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			bot.log.Info(
				"[EasyBot] 群 ", groupRecall.GroupID, " 中 ", groupRecall.UserID, " 撤回群聊消息: ", groupRecall.MessageID,
			)

			go bot.grMark(groupRecall)

			//回调vvvvvvvv
			if bot.On.GroupRecall != nil {
				go bot.On.GroupRecall(groupRecall)
			}

		case "friend_recall": //好友消息撤回
			friendRecall := &CQNoticeFriendRecall{
				Notice: notice,
			}

			err = json.Unmarshal(recv.Raw, friendRecall)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Notice.FriendRecall), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			bot.log.Info("[EasyBot] 好友 ", friendRecall.UserID, " 撤回私聊消息: ", friendRecall.MessageID)

			go bot.frMark(friendRecall)

			//回调vvvvvvvv
			if bot.On.FriendRecall != nil {
				go bot.On.FriendRecall(friendRecall)
			}

		case "group_card": //群名片变更
			groupCard := &CQNoticeGroupCard{
				Notice: notice,
			}

			err = json.Unmarshal(recv.Raw, groupCard)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Notice.GroupCard), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			bot.log.Info(
				"[EasyBot] 群 ", groupCard.GroupID, " 中 ", groupCard.UserID, " 更新了群名片: ", groupCard.CardOld, " -> ",
				groupCard.CardNew,
			)

			//回调vvvvvvvv
			if bot.On.GroupCard != nil {
				go bot.On.GroupCard(groupCard)
			}

		case "group_upload": //群文件上传
			groupUpload := &CQNoticeGroupUpload{
				Notice: notice,
			}

			err = json.Unmarshal(recv.Raw, groupUpload)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Notice.GroupUpload), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			bot.log.Info(
				"[EasyBot] 群 ", groupUpload.GroupID, " 中 ", groupUpload.UserID, " 上传了文件: ", groupUpload.File.Name, "(",
				groupUpload.File.Size/1024/1024, "MB) ", groupUpload.File.Url,
			)

			//回调vvvvvvvv
			if bot.On.GroupUpload != nil {
				go bot.On.GroupUpload(groupUpload)
			}

		case "offline_file": //离线文件上传
			offlineFile := &CQNoticeOfflineFile{
				Notice: notice,
			}

			err = json.Unmarshal(recv.Raw, offlineFile)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Notice.OfflineFile), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			bot.log.Info(
				"[EasyBot] 好友 ", offlineFile.UserID, " 发送了离线文件: ", offlineFile.File.Name, "(",
				offlineFile.File.Size/1024/1024, ") ", offlineFile.File.Url,
			)

			//回调vvvvvvvv
			if bot.On.OfflineFile != nil {
				go bot.On.OfflineFile(offlineFile)
			}

		case "notify": //系统通知
			notify := &CQNoticeNotify{
				Notice: notice,
			}

			err = json.Unmarshal(recv.Raw, notify)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.Notice.Notify), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			//回调vvvvvvvv
			if bot.On.Notify != nil {
				go bot.On.Notify(notify)
			}

			switch notify.SubType {
			case "poke":
				poke := &CQNoticeNotifyPoke{
					Notify: notify,
				}

				err = json.Unmarshal(recv.Raw, poke)
				if err != nil {
					bot.log.Warn(
						"[EasyBot] 反序列化出错(Event.Notice.Notify.Poke), 跳过处理, err: ", err,
						"\n    recv.Raw: ", string(recv.Raw),
						"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
					)
					return
				}

				bot.log.Info("[EasyBot] ", poke.OperatorID, " 戳了戳 ", poke.TargetID)

				//回调vvvvvvvv
				if bot.On.Poke != nil {
					go bot.On.Poke(poke)
				}

			default:
				bot.log.Info("[EasyBot] Unknown Event.Notice.Notify: ", string(recv.Raw))
			}
		default:
			bot.log.Info("[EasyBot] Unknown Event.Notice: ", string(recv.Raw))
		}
	case "meta_event": //元事件
		metaEvent := &CQMetaEvent{
			Bot:   bot,
			Event: event,
		}

		err = json.Unmarshal(recv.Raw, metaEvent)
		if err != nil {
			bot.log.Warn(
				"[EasyBot] 反序列化出错(Event.MetaEvent), 跳过处理, err: ", err,
				"\n    recv.Raw: ", string(recv.Raw),
				"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
			)
			return
		}

		switch metaEvent.MetaEventType {
		case "heartbeat":
			heartbeat := &CQMetaEventHeartbeat{
				MetaEvent: metaEvent,
			}

			err = json.Unmarshal(recv.Raw, heartbeat)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.MetaEvent.Heartbeat), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			go bot.handleHeartbeat(heartbeat)

			//回调vvvvvvvv
			if bot.On.Heartbeat != nil {
				go bot.On.Heartbeat(heartbeat)
			}

		case "lifecycle":
			lifecycle := &CQMetaEventLifecycle{
				MetaEvent: metaEvent,
			}

			err = json.Unmarshal(recv.Raw, lifecycle)
			if err != nil {
				bot.log.Warn(
					"[EasyBot] 反序列化出错(Event.MetaEvent.Lifecycle), 跳过处理, err: ", err,
					"\n    recv.Raw: ", string(recv.Raw),
					"\n    Unmarshal by gson: ", gson.New(recv.Raw).JSON("", ""),
				)
				return
			}

			go bot.handleLifecycle(lifecycle)

			//回调vvvvvvvv
			if bot.On.Lifecycle != nil {
				go bot.On.Lifecycle(lifecycle)
			}

		default:
			bot.log.Info("[EasyBot] Unknown Event.MetaEvent: ", string(recv.Raw))
		}
	default:
		bot.log.Info("[EasyBot] Unknown Event")
	}
}

// 屏蔽私聊
func (bot *CQBot) isBannedPrivate(uid int) bool {
	for _, bannedUid := range bot.BlackList.private {
		if bannedUid == uid {
			return true
		}
	}
	return false
}

// 屏蔽群聊检测
func (bot *CQBot) isBannedGroup(gid int) bool {
	for _, bannedGid := range bot.BlackList.group {
		if bannedGid == gid {
			return true
		}
	}
	return false
}

// 消息缓存
func (bot *CQBot) saveMsg(msg *CQMessage) {
	msg.Extra.TimeFormat = time.Unix(int64(msg.Event.Time), 0).Format(TimeLayout.T24)
	msg.Extra.AtWho = msg.collectAt()
	msg.Extra.MessageWithReply = msg.entityReply()
	switch msg.MessageType {
	case "group":
		bot.MTGMutex.Lock()
		if bot.MessageTableGroup[msg.GroupID] == nil {
			bot.MessageTableGroup[msg.GroupID] = make(map[int]*CQMessage)
		}
		bot.MessageTableGroup[msg.GroupID][msg.MessageID] = msg
		bot.MTGMutex.Unlock()

		bot.saveCardName(msg.Sender.GroupID, msg.Sender.UserID, msg.Sender.CardName)
		bot.saveNickName(msg.Sender.UserID, msg.Sender.NickName)

	case "private":
		bot.MTPMutex.Lock()
		if bot.MessageTablePrivate[msg.UserID] == nil {
			bot.MessageTablePrivate[msg.UserID] = make(map[int]*CQMessage)
		}
		bot.MessageTablePrivate[msg.UserID][msg.MessageID] = msg
		bot.MTPMutex.Unlock()

		bot.saveNickName(msg.Sender.UserID, msg.Sender.NickName)
	}
}

func (bot *CQBot) saveNickName(userId int, nickName string) {
	bot.NNTMutex.Lock()
	bot.NickNameTable[userId] = nickName
	bot.NNTMutex.Unlock()
}

func (bot *CQBot) saveCardName(groupId, userId int, cardName string) {
	bot.CNTMutex.Lock()
	if bot.CardNameTable[userId] == nil {
		bot.CardNameTable[userId] = make(map[int]string)
	}
	bot.CardNameTable[userId][groupId] = cardName
	bot.CNTMutex.Unlock()
}

var rmCardReg = regexp.MustCompile(`^\[CQ:json,data=|]$`)

func (msg *CQMessage) ToCardMsg() (cardMsg *CQCardMsg, err error) {
	s := unescape.Replace(msg.GetRawMessageOrMessage())
	s = rmCardReg.ReplaceAllString(s, "")
	cardMsg = &CQCardMsg{}
	err = json.Unmarshal([]byte(s), cardMsg)
	return
}

// @的人列表
func (msg *CQMessage) collectAt() (atWho []int) {
	matches := msg.RegFindAllStringSubmatch(regexp.MustCompile(`\[CQ:reply,id=(-?[0-9]*)]`)) //回复也算@
	if len(matches) > 0 {
		replyid, _ := strconv.Atoi(matches[0][1])
		switch msg.MessageType {
		case "group":
			msg.Bot.MTGMutex.Lock()
			if replyMsg := msg.Bot.MessageTableGroup[msg.GroupID][replyid]; replyMsg != nil {
				atWho = append(atWho, replyMsg.UserID)
			}
			msg.Bot.MTGMutex.Unlock()
		case "private":
			msg.Bot.MTPMutex.Lock()
			if replyMsg := msg.Bot.MessageTablePrivate[msg.UserID][replyid]; replyMsg != nil {
				atWho = append(atWho, replyMsg.UserID)
			}
			msg.Bot.MTPMutex.Unlock()
		}
	}
	matches = msg.RegFindAllStringSubmatch(regexp.MustCompile(`\[CQ:at,qq=(\d+)]`))
	if len(matches) > 0 {
		for _, match := range matches {
			atId, _ := strconv.Atoi(match[1])
			if isRepeated := func() bool { //检查重复收录
				for _, a := range atWho {
					if atId == a {
						return true
					}
				}
				return false
			}(); !isRepeated {
				atWho = append(atWho, atId)
			}
		}
	}
	return
}

// 具体化回复，go-cqhttp.extra-reply-data: true时不必要，但是开了那玩意又会导致回复带上原文又触发一遍机器人
func (msg *CQMessage) entityReply() (message string) {
	message = msg.GetRawMessageOrMessage()
	match := msg.RegFindAllStringSubmatch(regexp.MustCompile(`\[CQ:reply,id=(-?[0-9]*)]`))
	if len(match) > 0 {
		replyIdS := match[0][1]
		replyId, _ := strconv.Atoi(replyIdS)
		var replyMsg *CQMessage
		switch msg.MessageType {
		case "group":
			// replyMsg = msg.Bot.MessageTableGroup[msg.GroupID][replyId]
			r, err := msg.Bot.FetchGroupMsg(msg.GroupID, replyId)
			if err != nil {
				msg.Bot.log.Error("[EasyBot] 获取群消息出错: ", err)
				return
			}
			replyMsg = r
		case "private":
			// replyMsg = msg.Bot.MessageTablePrivate[msg.UserID][replyId]
			r, err := msg.Bot.FetchPrivateMsg(msg.UserID, replyId)
			if err != nil {
				msg.Bot.log.Error("[EasyBot] 获取私聊消息出错: ", err)
				return
			}
			replyMsg = r
		}
		if replyMsg == nil {
			msg.Bot.log.Warn("[EasyBot] 具体化回复遇到空指针")
			return
		}
		var replyCQ string
		if replyMsg.Event != nil {
			replyCQ = fmt.Sprint(
				"[CQ:reply,qq=", replyMsg.UserID, ",time=", replyMsg.Event.Time, ",text=", replyMsg.RawMessage, "]",
			)
		} else {
			replyCQ = fmt.Sprint("[CQ:reply,qq=", replyMsg.UserID, ",time=", replyMsg.Time, ",text=", replyMsg.RawMessage, "]")
		}
		msg.Bot.log.Debug("[EasyBot] 具体化回复了这条消息, reply: ", replyCQ)
		return strings.ReplaceAll(msg.GetRawMessageOrMessage(), match[0][0], replyCQ)
	}
	return
}

// 好友消息标记撤回
func (bot *CQBot) frMark(fr *CQNoticeFriendRecall) {
	var err error
	recalledMsg := &CQMessage{
		Bot: bot,
	}
	bot.MTGMutex.Lock()
	if msgTable := bot.MessageTableGroup[fr.UserID]; msgTable != nil {
		if msg := msgTable[fr.MessageID]; msg != nil {
			recalledMsg = msg
		}
	}
	bot.MTGMutex.Unlock()
	if recalledMsg == nil { //获取不到就/get_msg
		bot.log.Info("[EasyBot] 该消息 (", fr.MessageID, ") 未缓存, 尝试调用/get_msg")
		recalledMsg, err = bot.GetMsg(fr.MessageID) //阻塞直到成功返回
		if err != nil {
			bot.log.Error("[EasyBot] 获取用于标记撤回的消息失败(G)")
			return
		}
	}
	recalledMsg.Extra.Recalled = true
	recalledMsg.Extra.OperatorID = fr.UserID
	bot.MTPMutex.Lock()
	if bot.MessageTablePrivate[fr.UserID] == nil {
		bot.MessageTablePrivate[fr.UserID] = make(map[int]*CQMessage)
	}
	bot.MessageTablePrivate[fr.UserID][fr.MessageID] = recalledMsg
	bot.MTPMutex.Unlock()
}

// 群消息标记撤回
func (bot *CQBot) grMark(gr *CQNoticeGroupRecall) {
	var err error
	recalledMsg := &CQMessage{
		Bot: bot,
	}
	bot.MTGMutex.Lock()
	if msgTable := bot.MessageTableGroup[gr.GroupID]; msgTable != nil {
		if msg := msgTable[gr.MessageID]; msg != nil {
			recalledMsg = msg
		}
	}
	bot.MTGMutex.Unlock()
	if recalledMsg == nil { //获取不到就/get_msg
		bot.log.Info("[EasyBot] 该消息 (", gr.MessageID, ") 未缓存, 尝试调用/get_msg")
		recalledMsg, err = bot.GetMsg(gr.MessageID) //阻塞直到成功返回
		if err != nil {
			bot.log.Error("[EasyBot] 获取用于标记撤回的消息失败(G)")
			return
		}
	}
	recalledMsg.Extra.Recalled = true
	recalledMsg.Extra.OperatorID = gr.OperatorID
	bot.MTGMutex.Lock()
	if bot.MessageTableGroup[gr.GroupID] == nil {
		bot.MessageTableGroup[gr.GroupID] = make(map[int]*CQMessage)
	}
	bot.MessageTableGroup[gr.GroupID][gr.MessageID] = recalledMsg
	bot.MTGMutex.Unlock()
}

// 心跳
func (bot *CQBot) handleHeartbeat(hb *CQMetaEventHeartbeat) {
	bot.IsHeartbeatOK = true
	bot.HeartbeatInterval = hb.Interval
	bot.SelfID = hb.SelfID
	bot.Heartbeat <- struct{}{}
}

// 生命周期
func (bot *CQBot) handleLifecycle(lc *CQMetaEventLifecycle) {
	bot.SelfID = lc.SelfID
	bot.IsHeartbeatOK = true
	bot.HeartbeatInterval = lc.Interval
	go bot.heartbeatLoop()
	if lc.SelfID == 0 {
		bot.log.Error("[EasyBot] Unexpected Error: 'bot.SelfID == 0' in '(bot *CQBot) handleLifecycle()'")
	}
}

// 心跳监听
func (bot *CQBot) heartbeatLoop() {
	if bot.IsHeartbeatChecking {
		//保证单一
		return
	}
	bot.IsHeartbeatChecking = true
	bot.log.Info("[EasyBot] 开始监听 CQ 心跳")
	defer func() {
		bot.IsHeartbeatChecking = false
		bot.IsHeartbeatOK = false
		if !bot.IsExpectedTermination {
			bot.OnUnexpectedTermination()
		}
	}()

	for {
		select {
		case <-bot.Heartbeat:
			bot.HeartbeatCount++
			bot.log.Debug("[EasyBot] 心跳接收#", bot.HeartbeatCount)
			continue
		case <-time.After(time.Millisecond * time.Duration(bot.HeartbeatInterval+1000)):
			bot.HeartbeatLostCount++
			if bot.HeartbeatLostCount > 2 {
				bot.log.Error("[EasyBot] 心跳超时 ", bot.HeartbeatLostCount, " 次, 丢弃连接")
				bot.HeartbeatLostCount = 0
				err := bot.Conn.Close()
				if err != nil {
					bot.log.Error("[EasyBot] 断开连接失败: ", err)
				}
				return
			}
			bot.log.Error("[EasyBot] 心跳超时#", bot.HeartbeatLostCount)
		case <-bot.ConnLost:
			if !bot.IsExpectedTermination {
				bot.log.Error("[EasyBot] ws连接丢失")
			} else {
				bot.log.Info("[EasyBot] 主动断开了ws连接")
			}
			return
		}
	}
}

func (bot *CQBot) newApiCalling(action, echo string) *CQPost {
	return &CQPost{
		Bot: bot,
		Raw: map[string]any{
			"action": action,
			"echo":   echo,
		},
	}
}

// CallApi 调用api
func (bot *CQBot) CallApi(post *CQPost) (err error) {
	if post.Raw["echo"] == "" {
		bot.log.Error("[EasyBot] post.Raw[\"echo\"] 不可为空")
		err = e.needEcho
		return
	}
	err = bot.PostData(post)
	if err != nil {
		return err
	}
	echoChan := make(chan *CQApiResp)
	go func() {
		for {
			select {
			case <-bot.ApiCallNotice:
				if resp := bot.ApiCallResp[post.Raw["echo"].(string)]; resp != nil {
					bot.log.Debug("[EasyBot] 成功取到api调用echo")
					echoChan <- resp
					bot.log.Debug("[EasyBot] 成功传入echo")
					return
				}
			case <-time.After(bot.ApiCallTimeOut):
				bot.log.Error("[EasyBot] 监听echo超时")
				return
			}
		}
	}()
	select {
	case resp := <-echoChan:
		switch {
		case resp.RetCode == 0 && resp.Status == "ok":
			bot.log.Debug("[EasyBot] Api ", post.Raw["action"], " 调用成功")
		case resp.RetCode == 1 && resp.Status == "async":
			bot.log.Debug("[EasyBot] Api ", post.Raw["action"], " 已经提交异步处理")
		case resp.RetCode != 0 || resp.Msg != "" || resp.Wording != "":
			err = errors.New("[EasyBot] Api " + post.Raw["action"].(string) + " 调用失败: " + resp.Msg + " " + resp.Wording)
			bot.log.Error(err)
		}
	case <-time.After(bot.ApiCallTimeOut):
		err = errors.New("[EasyBot] Api " + post.Raw["action"].(string) + " 调用超时")
		bot.log.Error(err)
	}
	return
}

// CallApiAndListenEcho 调用api并监听echo
func (bot *CQBot) CallApiAndListenEcho(post *CQPost, echo string) (resp *CQApiResp, err error) {
	if err = bot.CallApi(post); err != nil {
		return nil, err
	}
	if resp = bot.ApiCallResp[echo]; resp == nil {
		return nil, e.noEcho
	}
	return
}

// FetchPrivateMsg 通过QQ号和消息ID获取私聊消息
// 优先从内存中读取缓存的私聊消息, 没有时调取/get_msg api
func (bot *CQBot) FetchPrivateMsg(userId, messageId int) (msg *CQMessage, err error) {
	bot.MTPMutex.Lock()
	defer bot.MTPMutex.Unlock()
	table := bot.MessageTablePrivate[userId]
	if table != nil {
		msg = table[messageId]
		if msg != nil {
			return msg, nil
		}
	}
	return bot.GetMsg(messageId)
}

// FetchGroupMsg 通过群号和消息ID获取群聊消息
// 优先从内存中读取缓存的群消息, 没有时调取/get_msg api
func (bot *CQBot) FetchGroupMsg(groupId, messageId int) (msg *CQMessage, err error) {
	bot.MTGMutex.Lock()
	defer bot.MTGMutex.Unlock()
	table := bot.MessageTableGroup[groupId]
	if table != nil {
		msg = table[messageId]
		if msg != nil {
			return msg, nil
		}
	}
	return bot.GetMsg(messageId)
}

type loginInfo struct {
	UserID   int    `json:"user_id"`
	NickName string `json:"nickname"`
}

// GetLoginInfo 获取登录号信息
func (bot *CQBot) GetLoginInfo() (userId int, nickname string, err error) {
	action := "get_login_info"
	echo := genEcho(action)
	p := bot.newApiCalling(action, echo)

	resp, err := bot.CallApiAndListenEcho(p, echo)
	if err != nil {
		return 0, "", err
	}
	respByte, err := json.Marshal(resp.Data)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 序列化出错(json.Marshal(resp.Data)), err: ", err,
			"\n    resp.Data: ", resp.Data,
			"\n    Marshal by gson: ", gson.New(resp.Data).JSON("", ""),
		)
		return 0, "", err
	}
	loginInfo := &loginInfo{}
	err = json.Unmarshal(respByte, loginInfo)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 反序列化出错(json.Unmarshal(respByte, loginInfo)), err: ", err,
			"\n    respByte: ", string(respByte),
			"\n    Unmarshal by gson: ", gson.New(respByte).JSON("", ""),
		)
		return 0, "", err
	}

	return loginInfo.UserID, loginInfo.NickName, nil
}

// GetMsg 调用api从gocq获取消息
// 注意: 通过此api调用返回的消息, 只存在"message"字段, 不存在raw_message字段, 可以再过一遍GetRawMessageOrMessage()
func (bot *CQBot) GetMsg(messageId int) (msg *CQMessage, err error) {
	action := "get_msg"
	echo := genEcho(action)
	p := bot.newApiCalling(action, echo)

	params := map[string]any{
		"message_id": messageId,
	}
	p.Raw["params"] = params

	resp, err := bot.CallApiAndListenEcho(p, echo)
	if err != nil {
		return nil, err
	}
	respByte, err := json.Marshal(resp.Data)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 序列化出错(json.Marshal(resp.Data)), err: ", err,
			"\n    resp.Data: ", resp.Data,
			"\n    Marshal by gson: ", gson.New(resp.Data).JSON("", ""),
		)
		return nil, err
	}
	msg = &CQMessage{}
	err = json.Unmarshal(respByte, msg)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 反序列化出错(json.Unmarshal(respByte, msg)), err: ", err,
			"\n    respByte: ", string(respByte),
			"\n    Unmarshal by gson: ", gson.New(respByte).JSON("", ""),
		)
		return nil, err
	}
	return
}

type downloadFile struct {
	File string `json:"file"`
}

func mapToStringsHeaders(origin map[string]string) (to []string) {
	for k, v := range origin {
		to = append(to, fmt.Sprint(k+"="+v))
	}
	return to
}

// DownloadFile 下载文件到gocq本地, 返回的路径可直接塞进CQ码里发送
// headers 可以为 []"k=v" 或 map["k"]"v"
// (最终都会转为 json 数组)
func (bot *CQBot) DownloadFile(url string, threadCount int, headers any) (path string, err error) {
	switch v := headers.(type) {
	case map[string]string:
		headers = mapToStringsHeaders(v)
	case []string:
	default:
		return "", e.wrongType
	}

	action := "download_file"
	echo := genEcho(action)
	p := bot.newApiCalling(action, echo)

	params := map[string]any{
		"url":          url,
		"thread_count": threadCount,
		"headers":      headers,
	}
	p.Raw["params"] = params

	resp, err := bot.CallApiAndListenEcho(p, echo)
	if err != nil {
		return "", err
	}
	respByte, err := json.Marshal(resp.Data)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 序列化出错(json.Marshal(resp.Data)), err: ", err,
			"\n    resp.Data: ", resp.Data,
			"\n    Marshal by gson: ", gson.New(resp.Data).JSON("", ""),
		)
		return "", err
	}
	file := &downloadFile{}
	err = json.Unmarshal(respByte, file)
	if err != nil {
		bot.log.Warn(
			"[EasyBot] 反序列化出错(json.Unmarshal(respByte, msg)), err: ", err,
			"\n    respByte: ", string(respByte),
			"\n    Unmarshal by gson: ", gson.New(respByte).JSON("", ""),
		)
		return "", err
	}
	return file.File, nil
}

// SendPrivateMsg 发送私聊消息
//
// otherParams:
//
// 0: groupId int `json:"group_id"` //主动发起临时会话时的来源群号
//
// 1: autoEscape bool `json:"auto_escape"` //不解析CQ码
func (bot *CQBot) SendPrivateMsg(userId int, message any, otherParams ...any) (err error) {
	action := "send_private_msg"
	echo := genEcho(action)
	p := bot.newApiCalling(action, echo)

	params := map[string]any{
		"user_id": userId,
		"message": message,
	}
	switch len(otherParams) {
	case 2:
		params["auto_escape"] = otherParams[1]
		fallthrough
	case 1:
		params["group_id"] = otherParams[0]
	case 0:
	default:
		bot.log.Error("[EasyBot] SendPrivateMsg() 非法的选参数量")
		return
	}
	p.Raw["params"] = params

	_, err = bot.CallApiAndListenEcho(p, echo)
	return
}

// SendPrivateMsgs SendPrivateMsg的并发批量操作, 中途发生错误不会中止
func (bot *CQBot) SendPrivateMsgs(userIds []int, message any, otherParams ...any) error {
	errs := make(map[int]error)
	var wg sync.WaitGroup
	for i, userId := range userIds {
		wg.Add(1)
		go func(i, userId int) {
			defer wg.Done()
			err := bot.SendPrivateMsg(userId, message, otherParams...)
			if err != nil {
				errs[i] = err
			}
		}(i, userId)
	}
	wg.Wait()
	if len(errs) > 0 {
		for i, err := range errs {
			bot.log.Error("[", i, "](", userIds[i], "): ", err)
		}
		return e.general
	}
	return nil
}

// SendPrivateMsgsSafe SendPrivateMsg的同步批量操作, 中途发生错误会直接中止并返回
func (bot *CQBot) SendPrivateMsgsSafe(userIds []int, message any, otherParams ...any) error {
	for _, userId := range userIds {
		err := bot.SendPrivateMsg(userId, message, otherParams...)
		if err != nil {
			return err
		}
	}
	return nil
}

// SendGroupMsg 发送群聊消息
//
// otherParams:
//
// 0: autoEscape bool `json:"auto_escape"` //不解析CQ码
func (bot *CQBot) SendGroupMsg(groupId int, message any, otherParams ...any) (err error) {
	action := "send_group_msg"
	echo := genEcho(action)
	p := bot.newApiCalling(action, echo)

	params := map[string]any{
		"group_id": groupId,
		"message":  message,
	}
	switch len(otherParams) {
	case 1:
		params["auto_escape"] = otherParams[0]
	case 0:
	default:
		bot.log.Error("[EasyBot] SendGroupMsg() 非法的选参数量, 取消执行")
		return
	}
	p.Raw["params"] = params

	_, err = bot.CallApiAndListenEcho(p, echo)
	return
}

// SendGroupMsgs SendGroupMsg的并发批量操作, 中途发生错误不会中止
func (bot *CQBot) SendGroupMsgs(groupIds []int, message any, otherParams ...any) error {
	errs := make(map[int]error)
	for i, groupId := range groupIds {
		go func(i, groupId int) {
			err := bot.SendGroupMsg(groupId, message, otherParams...)
			if err != nil {
				errs[i] = err
			}
		}(i, groupId)
	}
	if len(errs) > 0 {
		for i, err := range errs {
			bot.log.Error("[", i, "](", groupIds[i], "): ", err)
		}
		return e.general
	}
	return nil
}

// SendGroupMsgsSafe SendGroupMsg的同步批量操作, 中途发生错误会直接中止并返回
func (bot *CQBot) SendGroupMsgsSafe(groupIds []int, message any, otherParams ...any) error {
	for _, groupId := range groupIds {
		err := bot.SendGroupMsg(groupId, message, otherParams...)
		if err != nil {
			return err
		}
	}
	return nil
}

// NewMsgForwardNode 通过消息id直接引用消息作为消息节点
func NewMsgForwardNode(msgId any) CQForwardNode {
	return CQForwardNode{
		"type": "node",
		"data": map[string]any{
			"id": msgId,
		},
	}
}

// NewCustomForwardNodeOSR 创建自定义消息节点_OpenShamrock
func NewCustomForwardNodeOSR(content string) CQForwardNode {
	return CQForwardNode{
		"type": "node",
		"data": map[string]any{
			"content": content,
		},
	}
}

// NewCustomForwardNode 创建自定义消息节点
//
//	type nodeData struct { //标准的gocq合并转发消息节点
//		name    string //消息发送者名字
//		uin     int    //消息发送者头像
//		content string //自定义消息内容
//		time    int64  //秒级时间戳(为0时使用当前时间)
//		seq     int64  //起始消息序号(为0时不上报)
//	}
func NewCustomForwardNode(name string, uin int, content string, timestamp, seq int64) CQForwardNode {
	if timestamp == 0 {
		timestamp = time.Now().Unix()
	}
	node := CQForwardNode{
		"type": "node",
		"data": map[string]any{
			"name":    name,
			"uin":     uin,
			"content": content,
			"time":    timestamp,
		},
	}
	if seq != 0 {
		node["seq"] = seq
	}
	return node
}

// AppendForwardMsg 对合并转发消息追加消息节点
//
// 也可以塞个 nil 然后当 NewForwardMsg() 用
func AppendForwardMsg(forwardMsg CQForwardMsg, nodes ...CQForwardNode) CQForwardMsg {
	for _, node := range nodes {
		forwardMsg = append(forwardMsg, node)
	}
	return forwardMsg
}

// NewForwardMsg 合并多个消息节点, 创建合并转发消息
func NewForwardMsg(nodes ...CQForwardNode) CQForwardMsg {
	return AppendForwardMsg(nil, nodes...)
}

// FastNewForwardMsg 快速创建合并转发消息
//
// 所有 content 沿用同一其他参数
//
//	type nodeData struct { //标准的gocq合并转发消息节点
//		name    string //消息发送者名字
//		uin     int    //消息发送者头像
//		content string //自定义消息内容
//		time    int64  //秒级时间戳(为0时使用当前时间)
//		seq     int64  //起始消息序号(为0时不上报)
//	}
func FastNewForwardMsg(name string, uin int, timestamp, seq int64, content ...string) CQForwardMsg {
	var forwardMsg CQForwardMsg
	if len(content) == 0 {
		return nil
	}
	for _, content_ := range content {
		forwardMsg = AppendForwardMsg(
			forwardMsg,
			NewCustomForwardNode(name, uin, content_, timestamp, seq),
		)
	}
	return forwardMsg
}

// SendPrivateForwardMsg 发送私聊合并转发消息
func (bot *CQBot) SendPrivateForwardMsg(userId int, nodes CQForwardMsg) (err error) {
	action := "send_private_forward_msg"
	echo := genEcho(action)
	p := bot.newApiCalling(action, echo)

	params := map[string]any{
		"user_id":  userId,
		"messages": nodes,
	}
	p.Raw["params"] = params

	_, err = bot.CallApiAndListenEcho(p, echo)
	return
}

// SendPrivateForwardMsgs SendPrivateForwardMsg的批量操作, 中途发生错误不会中止
func (bot *CQBot) SendPrivateForwardMsgs(userIds []int, nodes CQForwardMsg) (err error) {
	errs := make(map[int]error)
	for i, userId := range userIds {
		go func(i, userId int) {
			errs[i] = bot.SendPrivateForwardMsg(userId, nodes)
		}(i, userId)
	}
	if len(errs) > 0 {
		for i, err := range errs {
			bot.log.Error("[", i, "](", userIds[i], "): ", err)
		}
		return e.general
	}
	return nil
}

// SendGroupForwardMsg 发送群聊合并转发消息
func (bot *CQBot) SendGroupForwardMsg(groupId int, nodes CQForwardMsg) (err error) {
	action := "send_group_forward_msg"
	echo := genEcho(action)
	p := bot.newApiCalling(action, echo)

	params := map[string]any{
		"group_id": groupId,
		"messages": nodes,
	}
	p.Raw["params"] = params

	_, err = bot.CallApiAndListenEcho(p, echo)
	return
}

// SendGroupForwardMsgs SendGroupForwardMsg的批量操作, 中途发生错误不会中止
func (bot *CQBot) SendGroupForwardMsgs(groupIds []int, nodes CQForwardMsg) (err error) {
	errs := make(map[int]error)
	for i, groupId := range groupIds {
		go func(i, groupId int) {
			errs[i] = bot.SendGroupForwardMsg(groupId, nodes)
		}(i, groupId)
	}
	if len(errs) > 0 {
		for i, err := range errs {
			bot.log.Error("[", i, "](", groupIds[i], "): ", err)
		}
		return e.general
	}
	return nil
}

// GetRawMessageOrMessage raw_message字段非空则返回raw_message，否则返回message字段
//
// 通过/get_msg api等途径获取到的消息体可能不存在message_raw字段
func (ctx *CQMessage) GetRawMessageOrMessage() string {
	if ctx.RawMessage != "" {
		return ctx.RawMessage
	}
	return fmt.Sprint(ctx.Message)
}

// StringsMatch 字符串匹配
//
//	return ctx.GetRawMessageOrMessage() == str
func (ctx *CQMessage) StringsMatch(str string) bool {
	return ctx.GetRawMessageOrMessage() == str
}

// RegFindAllStringSubmatch 正则完全匹配
//
//	return reg.FindAllStringSubmatch(ctx.GetRawMessageOrMessage(), -1)
func (ctx *CQMessage) RegFindAllStringSubmatch(reg *regexp.Regexp) [][]string {
	return reg.FindAllStringSubmatch(ctx.GetRawMessageOrMessage(), -1)
}

// RegReplaceAll 正则完全匹配替换
//
//	return reg.ReplaceAllString(ctx.GetRawMessageOrMessage(), replaceTo)
func (ctx *CQMessage) RegReplaceAll(reg *regexp.Regexp, replaceTo string) string {
	return reg.ReplaceAllString(ctx.GetRawMessageOrMessage(), replaceTo)
}

// StringsContains 字符串包含
//
//	return strings.Contains(ctx.GetRawMessageOrMessage(), substr)
func (ctx *CQMessage) StringsContains(substr string) bool {
	return strings.Contains(ctx.GetRawMessageOrMessage(), substr)
}

// StringsReplace 字符串替换
//
//	return replacer.Replace(ctx.GetRawMessageOrMessage())
func (ctx *CQMessage) StringsReplace(replacer *strings.Replacer) string {
	return replacer.Replace(ctx.GetRawMessageOrMessage())
}

// IsSU 是否来自超级用户
func (ctx *CQMessage) IsSU() bool {
	for _, su := range ctx.Bot.SuperUsers {
		if ctx.UserID == su {
			return true
		}
	}
	return false
}

// IsGroup 是否来自群聊
//
//	return ctx.MessageType == "group"
func (ctx *CQMessage) IsGroup() bool {
	return ctx.MessageType == "group"
}

// IsPrivate 是否来自私聊
//
//	return ctx.MessageType == "private"
func (ctx *CQMessage) IsPrivate() bool {
	return ctx.MessageType == "private"
}

// IsPrivateSU 是否来自超级用户私聊
//
//	return ctx.IsPrivate() && ctx.IsSU()
func (ctx *CQMessage) IsPrivateSU() bool {
	return ctx.IsPrivate() && ctx.IsSU()
}

// ReplaceNickName 替换消息中的机器人名字
//
// new: 要替换为的字符串
//
// n: 替换次数
func (ctx *CQMessage) ReplaceNickName(new string, n int) *CQMessage {
	for _, nm := range ctx.Bot.NickName {
		ctx.RawMessage = strings.Replace(ctx.GetRawMessageOrMessage(), nm, new, n)
	}
	return ctx
}

// IsToMe 是否提及了Bot ( 回复、at、bot别名、私聊 )
//
// bot别名可能会导致......?
func (ctx *CQMessage) IsToMe() bool {
	isReplyMe := func() bool {
		replyMsg, err := ctx.GetReplyedMsg()
		if err != nil {
			return false
		}
		return replyMsg.UserID == ctx.Bot.SelfID
	}()

	isAtMe := func() bool {
		match := ctx.RegFindAllStringSubmatch(regexp.MustCompile(fmt.Sprintf(`\[CQ:at,qq=%d]`, ctx.Bot.SelfID)))
		return len(match) > 0
	}()

	isCallMe := func() bool {
		for _, n := range ctx.Bot.NickName {
			if ctx.StringsContains(n) {
				return true
			}
		}
		return false
	}()

	return isReplyMe || isAtMe || isCallMe || ctx.IsPrivate() //私聊永远都是
}

// IsCardMsg 是否为卡片消息
//
//	return ctx.IsJsonMsg() || ctx.IsXmlMsg()
func (ctx *CQMessage) IsCardMsg() bool {
	return ctx.IsJsonMsg() || ctx.IsXmlMsg()
}

// IsJsonMsg 是否为json卡片消息
func (ctx *CQMessage) IsJsonMsg() bool {
	msg := ctx.GetRawMessageOrMessage()
	if len(msg) < 8 {
		return false
	}
	return msg[1:8] == "CQ:json"
}

// IsXmlMsg 是否为xml卡片消息
func (ctx *CQMessage) IsXmlMsg() bool {
	msg := ctx.GetRawMessageOrMessage()
	if len(msg) < 7 {
		return false
	}
	return msg[1:7] == "CQ:xml"
}

// GetCardOrNickname 群名片为空则返回昵称
func (ctx *CQMessage) GetCardOrNickname() string {
	if strings.TrimSpace(ctx.Sender.CardName) != "" {
		return ctx.Sender.CardName
	}
	return ctx.Sender.NickName
}

// GetReplyedMsg 获取回复的消息
func (ctx *CQMessage) GetReplyedMsg() (replyedMsg *CQMessage, err error) {
	matches := ctx.RegFindAllStringSubmatch(regexp.MustCompile(`\[CQ:reply,id=(-?[0-9]*)]`))
	if len(matches) == 0 {
		return nil, errors.New("NO REPLY MESSAGE")
	}
	replyId, _ := strconv.Atoi(matches[0][1])
	switch ctx.MessageType {
	case "private":
		return ctx.Bot.FetchPrivateMsg(ctx.UserID, replyId)
	case "group":
		return ctx.Bot.FetchGroupMsg(ctx.GroupID, replyId)
	default:
		return nil, e.unknownMsgType
	}
}

// SendMsg 根据上下文发送消息
func (ctx *CQMessage) SendMsg(message ...any) (err error) {
	switch ctx.MessageType {
	case "private":
		return ctx.Bot.SendPrivateMsg(ctx.UserID, fmt.Sprint(message...))
	case "group":
		return ctx.Bot.SendGroupMsg(ctx.GroupID, fmt.Sprint(message...))
	default:
		return e.unknownMsgType
	}
}

// SendMsgReply 根据上下文发送回复消息
func (ctx *CQMessage) SendMsgReply(message ...any) (err error) {
	return ctx.SendMsg(ctx.Bot.Utils.Format.Reply(ctx.MessageID), fmt.Sprint(message...))
}

// SendForwardMsg 根据上下文发送合并转发消息
func (ctx *CQMessage) SendForwardMsg(nodes CQForwardMsg) (err error) {
	switch ctx.MessageType {
	case "private":
		return ctx.Bot.SendPrivateForwardMsg(ctx.UserID, nodes)
	case "group":
		return ctx.Bot.SendGroupForwardMsg(ctx.GroupID, nodes)
	default:
		return e.unknownMsgType
	}
}

// Unescape 对raw_message进行反转义, 没有的话从message取
func (ctx *CQMessage) Unescape() *CQMessage {
	ctx.RawMessage = unescape.Replace(ctx.GetRawMessageOrMessage())
	return ctx
}

// TrimSpace 清理两侧的空格、换行
func (ctx *CQMessage) TrimSpace() *CQMessage {
	ctx.RawMessage = strings.TrimSpace(ctx.GetRawMessageOrMessage())
	return ctx
}

// 小工具
type utilsFunc struct {
	bot *CQBot

	Format *formater
}

// 格式化为CQ码
type formater struct {
	utils *utilsFunc
}

// fmt.Sprintf("[CQ:reply,id=%d]", id)
func (f *formater) Reply(id int) string {
	return fmt.Sprintf("[CQ:reply,id=%d]", id)
}

// 编码自定义回复至 CQcode
func (f *formater) CustomReply(text string, qq int, timestamp int, seq int) string {
	if text == "" {
		text = "<内部错误：未指定自定义回复内容>"
	}
	if qq == 0 {
		qq = f.utils.bot.SelfID
	}
	if timestamp == 0 {
		timestamp = int(time.Now().Unix())
	}
	if seq != 0 {
		return fmt.Sprintf("[CQ:reply,text=%s,qq=%d,time=%d,seq=%d]", text, qq, timestamp, seq)
	}
	return fmt.Sprintf("[CQ:reply,text=%s,qq=%d,time=%d]", text, qq, timestamp)
}

// 将 base64 图片数据编码至 CQcode
func (f *formater) ImageBase64(imageB64 string) string {
	return "[CQ:image,file=base64://" + imageB64 + "]"
}

func (f *formater) ImageUrl(url string, params ...string) string {
	param := ""
	for _, p := range params {
		param += p
	}
	return "[CQ:image,file=" + url + "]"
}

func (f *formater) ImageLocal(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "<内部错误：图片读取失败: " + err.Error() + " >"
	}
	return f.Image(data)
}

// 将图片数据以 base64 的方式编码至 CQcode
func (f *formater) Image(data []byte) string {
	imageB64 := base64.StdEncoding.EncodeToString(data)
	return "[CQ:image,file=base64://" + imageB64 + "]"
}

// fmt.Sprintf("[CQ:video,file=%s]", path)
func (f *formater) Video(path string) string {
	return fmt.Sprintf("[CQ:video,file=%s]", path)
}

/*
将音频 base64 编码至 CQcode,

sendDirectly 为 true 时: 不调用 ffmpeg 转换至 amr
*/
func (f *formater) VocalBase64(audioB64 string, sendDirectly bool) string {
	if sendDirectly {
		return "[CQ:record,file=base64://" + audioB64 + "]"
	}

	data, err := base64.StdEncoding.DecodeString(audioB64)
	if err != nil {
		return "<内部错误：转换amr时base64解码失败: " + err.Error() + ">"
	}
	amrData, err := f.utils.Ffmpeg2amr(data)
	if err != nil {
		return "<内部错误：调用ffmpeg转换amr失败: " + err.Error() + ">"
	}
	return "[CQ:record,file=base64://" + base64.StdEncoding.EncodeToString(amrData) + "]"
}

/*
读取文件并将音频数据以 base64 的方式编码至 CQcode,

sendDirectly 为 true 时: 不调用 ffmpeg 转换至 amr
*/
func (f *formater) VocalLocal(path string, sendDirectly bool) string {
	audioData, err := os.ReadFile(path)
	if err != nil {
		return "<内部错误：音频读取失败: " + err.Error() + ">"
	}
	return f.Vocal(audioData, sendDirectly)
}

/*
将音频数据以 base64 的方式编码至 CQcode,

sendDirectly 为 true 时: 不调用 ffmpeg 转换至 amr
*/
func (f *formater) Vocal(data []byte, sendDirectly bool) string {
	if sendDirectly {
		return "[CQ:record,file=base64://" + base64.StdEncoding.EncodeToString(data) + "]"
	}

	amrData, err := f.utils.Ffmpeg2amr(data)
	if err != nil {
		return "<内部错误：调用ffmpeg转换amr失败: " + err.Error() + ">"
	}
	return "[CQ:record,file=base64://" + base64.StdEncoding.EncodeToString(amrData) + "]"
}

// 调用 path 中的 ffmpeg 转换音频至 amr 格式
func (u *utilsFunc) Ffmpeg2amr(wav []byte) (amr []byte, err error) {
	cmd := exec.Command("ffmpeg", "-f", "wav", "-i", "pipe:0", "-ar", "8000", "-ac", "1", "-f", "amr", "pipe:1")
	cmd.Stdin = strings.NewReader(string(wav))
	amr, err = cmd.Output()
	if err != nil {
		u.bot.log.Error("[EasyBot] FFmpeg 转换 amr 失败: ", err)
		return nil, err
	}
	return
}

func genEcho(prefix string) string {
	return prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func deleteValueInSlice[T comparable](slice []T, value T) []T {
	for i := 0; i < len(slice); i++ {
		if slice[i] == value {
			if len(slice) == 1 {
				return []T{}
			}
			slice = append(slice[:i], slice[i+1:]...)
			i--
		}
	}
	return slice
}
