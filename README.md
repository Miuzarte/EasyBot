# EasyBot
### 轻松接入go-cqhttp

在这个~~屎山~~库你将会遇到：结构体成员瞎jb导出、结构体父子关系瞎jb继承、空指针瞎jb报(应该)、API没写完

大概的样子：

(设置用的函数都能连着写)

```go
import (
	"github.com/Miuzarte/EasyBot"
	"github.com/sirupsen/logrus"
)

var bot = EasyBot.New()

func main() {
	bot.SetWsUrl("127.0.0.1:9820"). //不以"ws://"开头会自动补上
		AddSU(1145141919).
		SetLogLevel(logrus.ErrorLevel). //自身输出的日志, Info及以上会报告接收到的消息
		EnableOnlineNotification(true). //上线通知超级用户
		EnableOfflineNotification(false)

	bot.OnData(func(data *EasyBot.CQRecv) {
			logrus.Debug("下发数据: ", string(data.Raw))
		}).
		OnTerminateUnexpectedly(func() {
			bot.Connect(true) //意外断开自动重连
		}).
		OnMessage(func(msg *EasyBot.CQMessage) {
			handleMessage(msg)
		})

	err := bot.Connect(true) //建立连接失败时：(true)无限尝试重连、(true, 4)尝试重连四次
	if err != nil {
		logrus.Error("bot.Connect err: ", err)
	}
	defer bot.Disconnect()
}

func handleMessage(msg *EasyBot.CQMessage) {
	switch msg.MessageType {
	case "private":
		logrus.Infof("收到 %s(%d) 的消息(%d): %s",
			msg.Sender.NickName,
			msg.UserID,
			msg.MessageID,
			msg.RawMessage)
	case "group":
		logrus.Infof("在 %d 收到 %s(%s %d) 的群聊消息(%d): %s",
			msg.GroupID,
			msg.Sender.CardName,
			msg.Sender.NickName,
			msg.UserID,
			msg.MessageID,
			msg.RawMessage)
	}
}
```