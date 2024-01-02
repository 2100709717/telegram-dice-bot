package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-redis/redis/v8"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gorm.io/gorm"
	"log"
	"strings"
	"telegram-dice-bot/internal/common"
	"telegram-dice-bot/internal/enums"
	"telegram-dice-bot/internal/model"
	"telegram-dice-bot/internal/utils"
	"time"
)

const (
	RedisButtonCallBackDataKey  = "BUTTON_CALLBACK_DATA:%s"
	RedisBotPrivateChatCacheKey = "BOT_PRIVATE_CHAT_CACHE:TG_USER_ID:%v"
)

func sendMessage(bot *tgbotapi.BotAPI, chattable tgbotapi.Chattable) (tgbotapi.Message, error) {
	sentMsg, err := bot.Send(chattable)
	if err != nil {
		log.Println("发送消息异常:", err)
		return sentMsg, err
	}
	return sentMsg, nil
}

func blockedOrKicked(err error, chatId int64) {
	if err != nil {
		if strings.Contains(err.Error(), "Forbidden: bot was blocked") {
			log.Printf("The bot was blocked ChatId: %v", chatId)
			// 对话已被用户阻止
		} else if strings.Contains(err.Error(), "Forbidden: bot was kicked") {
			log.Printf("The bot was kicked ChatId: %v", chatId)
			// 对话已被踢出群聊 修改群配置
			_, err := model.UpdateChatGroupStatusByTgChatId(db, &model.ChatGroup{
				TgChatGroupId:   chatId,
				ChatGroupStatus: enums.Kicked.Value,
			})
			if err != nil {
				log.Printf("群配置修改失败 TgChatId: %v", chatId)
				return
			}
		}
	}

}

// getChatMember 获取有关聊天成员的信息。
func getChatMember(bot *tgbotapi.BotAPI, chatID int64, userId int64) (tgbotapi.ChatMember, error) {
	chatMemberConfig := tgbotapi.ChatConfigWithUser{
		ChatID: chatID,
		UserID: userId,
	}

	return bot.GetChatMember(tgbotapi.GetChatMemberConfig{ChatConfigWithUser: chatMemberConfig})
}

func buildDefaultInlineKeyboardMarkup(bot *tgbotapi.BotAPI) *tgbotapi.InlineKeyboardMarkup {
	newInlineKeyboardMarkup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👨🏻‍💼我加入的群", enums.CallbackJoinedGroup.Value),
			tgbotapi.NewInlineKeyboardButtonData("👮🏻‍♂️我管理的群", enums.CallbackAdminGroup.Value)),
	)
	return &newInlineKeyboardMarkup
}

func buildGameplayConfigInlineKeyboardButton(chatGroup *model.ChatGroup, callbackDataQueryString string) ([]tgbotapi.InlineKeyboardButton, error) {

	var inlineKeyboardButton []tgbotapi.InlineKeyboardButton
	if chatGroup.GameplayType == enums.QuickThere.Value {
		// 查询该配置
		quickThereConfig, err := model.QueryQuickThereConfigByChatGroupId(db, chatGroup.Id)

		if err != nil {
			return nil, err
		}
		inlineKeyboardButton = tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("⚖️简易倍率: %v 倍", quickThereConfig.SimpleOdds), fmt.Sprintf("%s%s", enums.CallbackUpdateQuickThereSimpleOdds.Value, callbackDataQueryString)),
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("⚖️豹子倍率: %v 倍", quickThereConfig.TripletOdds), fmt.Sprintf("%s%s", enums.CallbackUpdateQuickThereTripletOdds.Value, callbackDataQueryString)),
		)
	}

	return inlineKeyboardButton, nil
}

func buildJoinedGroupMsg(query *tgbotapi.CallbackQuery) (*tgbotapi.EditMessageTextConfig, error) {
	fromUser := query.From
	fromChatId := query.Message.Chat.ID
	messageId := query.Message.MessageID

	var sendMsg tgbotapi.EditMessageTextConfig
	var inlineKeyboardRows [][]tgbotapi.InlineKeyboardButton

	// 查询当前人的信息
	chatGroupUserQuery := &model.ChatGroupUser{
		// 查询用户信息
		TgUserId: fromUser.ID,
		IsLeft:   0,
	}

	chatGroupUsers, err := chatGroupUserQuery.ListByTgUserIdAndIsLeft(db)
	if err != nil {
		log.Printf("TgUserId %v 查询群组异常 err %s", fromUser.ID, err.Error())
		return nil, err
	}
	if len(chatGroupUsers) == 0 {
		// 没有找到记录
		sendMsg = tgbotapi.NewEditMessageText(fromChatId, messageId, "您暂无加入的群!")
	} else {

		// 查询该用户的ChatGroupId
		var chatGroupIds []string
		for _, user := range chatGroupUsers {
			chatGroupIds = append(chatGroupIds, user.ChatGroupId)
		}

		chatGroups, err := model.ListChatGroupByIds(db, chatGroupIds)
		if err != nil {
			log.Printf("chatGroupIds %v 查询群组异常 err %s", chatGroupIds, err.Error())
			return nil, err
		}

		sendMsg = tgbotapi.NewEditMessageText(fromChatId, messageId, fmt.Sprintf("您有%v个加入的群:", len(chatGroups)))

		for _, group := range chatGroups {
			callbackDataKey, err := ButtonCallBackDataAddRedis(map[string]string{
				"chatGroupId": group.Id,
			})
			if err != nil {
				log.Println("内联键盘回调参数存入redis异常", err.Error())
				return nil, err
			}

			callbackDataQueryString := utils.MapToQueryString(map[string]string{
				"callbackDataKey": callbackDataKey,
			})

			inlineKeyboardRows = append(inlineKeyboardRows,
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("👥 %s", group.TgChatGroupTitle), fmt.Sprintf("%s%s", enums.CallbackChatGroupInfo.Value, callbackDataQueryString)),
				),
			)
		}
	}
	inlineKeyboardRows = append(inlineKeyboardRows,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️返回", enums.CallbackMainMenu.Value),
		),
	)

	// 组装列表数据
	newInlineKeyboardMarkup := tgbotapi.NewInlineKeyboardMarkup(
		inlineKeyboardRows...,
	)

	sendMsg.ReplyMarkup = &newInlineKeyboardMarkup

	return &sendMsg, nil
}

func buildAdminGroupMsg(query *tgbotapi.CallbackQuery) (*tgbotapi.EditMessageTextConfig, error) {
	chatId := query.Message.Chat.ID
	fromUser := query.From
	messageId := query.Message.MessageID

	var sendMsg tgbotapi.EditMessageTextConfig
	var inlineKeyboardRows [][]tgbotapi.InlineKeyboardButton

	inlineKeyboardRows = append(inlineKeyboardRows,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕点击添加新的群组", enums.CallbackAddAdminGroup.Value),
		),
	)

	// 查询当前消息来源人关联的群聊
	chatGroupAdmins, err := model.ListChatGroupAdminByAdminTgUserId(db, fromUser.ID)
	if len(chatGroupAdmins) == 0 {
		sendMsg = tgbotapi.NewEditMessageText(chatId, messageId, "您暂无管理的群!")
	} else if err != nil {
		log.Printf("TgUserId %v 查询管理群列表异常 %s ", chatId, err.Error())
		return nil, errors.New("查询管理群列表异常")
	} else {
		sendMsg = tgbotapi.NewEditMessageText(chatId, messageId, fmt.Sprintf("您有%v个管理的群:", len(chatGroupAdmins)))
		for _, chatGroupAdmin := range chatGroupAdmins {
			// 查找该群的信息
			ChatGroup, err := model.QueryChatGroupById(db, chatGroupAdmin.ChatGroupId)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				log.Printf("群TgChatId %v 未查询到数据 ", chatId)
				continue
			} else if err != nil {
				log.Printf("群TgChatId %v 查找异常 %s", chatId, err.Error())
				continue
			} else {
				callbackDataKey, err := ButtonCallBackDataAddRedis(map[string]string{
					"chatGroupId": ChatGroup.Id,
				})
				if err != nil {
					log.Println("内联键盘回调参数存入redis异常", err.Error())
					return nil, err
				}

				callbackDataQueryString := utils.MapToQueryString(map[string]string{
					"callbackDataKey": callbackDataKey,
				})

				inlineKeyboardRows = append(inlineKeyboardRows,
					tgbotapi.NewInlineKeyboardRow(
						tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("👥 %s", ChatGroup.TgChatGroupTitle), fmt.Sprintf("%s%s", enums.CallbackChatGroupConfig.Value, callbackDataQueryString))),
				)
			}
		}
	}
	inlineKeyboardRows = append(inlineKeyboardRows,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️返回", enums.CallbackMainMenu.Value),
		),
	)

	// 组装列表数据
	newInlineKeyboardMarkup := tgbotapi.NewInlineKeyboardMarkup(
		inlineKeyboardRows...,
	)

	sendMsg.ReplyMarkup = &newInlineKeyboardMarkup
	return &sendMsg, nil
}

func checkGroupAdmin(chatGroupId string, tgUserId int64) error {
	_, err := model.QueryChatGroupAdminByChatGroupIdAndTgUserId(db, chatGroupId, tgUserId)
	if err != nil {
		return err
	}
	return nil
}

func buildGameplayTypeInlineKeyboardButton(chatGroupId string) ([][]tgbotapi.InlineKeyboardButton, error) {

	ChatGroup, err := model.QueryChatGroupById(db, chatGroupId)

	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("群ChatGroupId %v 该群未初始化过配置 ", chatGroupId)
		return nil, err
	} else if err != nil {
		log.Printf("群ChatGroupId %v 查找异常 %s", chatGroupId, err.Error())
		return nil, err
	}

	var inlineKeyboardRows [][]tgbotapi.InlineKeyboardButton

	for key, value := range enums.GameplayTypeMap {

		callBackDataKey, err := ButtonCallBackDataAddRedis(map[string]string{
			"chatGroupId":  chatGroupId,
			"gameplayType": key,
		})

		if err != nil {
			log.Println("内联键盘回调参数存入redis异常", err.Error())
			return nil, err
		}

		buttonDataText := value.Name

		if ChatGroup.GameplayType == key {
			buttonDataText = fmt.Sprintf("%s✅", buttonDataText)
		}

		callBackDataQueryString := utils.MapToQueryString(map[string]string{
			"callbackDataKey": callBackDataKey,
		})

		inlineKeyboardRows = append(inlineKeyboardRows,
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(buttonDataText, fmt.Sprintf("%s%s", enums.CallbackUpdateGameplayType.Value, callBackDataQueryString)),
			),
		)
	}

	callbackDataKey, err := ButtonCallBackDataAddRedis(map[string]string{
		"chatGroupId": ChatGroup.Id,
	})

	if err != nil {
		log.Println("内联键盘回调参数存入redis异常", err.Error())
		return nil, err
	}

	callBackDataQueryString := utils.MapToQueryString(map[string]string{
		"callbackDataKey": callbackDataKey,
	})

	inlineKeyboardRows = append(inlineKeyboardRows,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️返回", fmt.Sprintf("%s%s", enums.CallbackChatGroupConfig.Value, callBackDataQueryString)),
		),
	)
	return inlineKeyboardRows, nil
}

func ButtonCallBackDataAddRedis(queryMap map[string]string) (string, error) {
	jsonBytes, err := json.Marshal(queryMap)
	if err != nil {
		return "", err
	}

	id, err := utils.NextID()
	if err != nil {
		return "", err
	}

	redisKey := fmt.Sprintf(RedisButtonCallBackDataKey, id)

	// 存入redis
	err = redisDB.Set(redisDB.Context(), redisKey, string(jsonBytes), 1*time.Hour).Err()

	return id, nil
}

func ButtonCallBackDataQueryFromRedis(key string) (map[string]string, error) {

	redisKey := fmt.Sprintf(RedisButtonCallBackDataKey, key)
	result := redisDB.Get(redisDB.Context(), redisKey)
	if errors.Is(result.Err(), redis.Nil) || result == nil {
		log.Printf("键 %s 不存在", redisKey)
		return nil, result.Err()
	} else if result.Err() != nil {
		log.Println("获取值时发生错误:", result.Err())
		return nil, result.Err()
	} else {
		var m map[string]string
		mapString, _ := result.Result()
		err := json.Unmarshal([]byte(mapString), &m)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
}

func PrivateChatCacheAddRedis(tgUserID int64, botPrivateChatCache *common.BotPrivateChatCache) error {

	jsonBytes, err := json.Marshal(botPrivateChatCache)
	if err != nil {
		return err
	}

	redisKey := fmt.Sprintf(RedisBotPrivateChatCacheKey, tgUserID)

	// 存入redis
	return redisDB.Set(redisDB.Context(), redisKey, string(jsonBytes), 24*time.Hour).Err()

}

func buildChatGroupInlineKeyboardMarkup(query *tgbotapi.CallbackQuery, chatGroup *model.ChatGroup) (*tgbotapi.InlineKeyboardMarkup, error) {

	chatId := query.Message.Chat.ID

	gameplayType, b := enums.GetGameplayType(chatGroup.GameplayType)
	if !b {
		log.Printf("GameplayType %v 群配置玩法查询异常", chatGroup.GameplayType)
		return nil, errors.New("群配置玩法查询异常")
	}
	gameplayStatus, b := enums.GetGameplayStatus(chatGroup.GameplayStatus)
	if !b {
		log.Printf("GameplayStatus %v 群配置玩法查询异常", chatGroup.GameplayStatus)
		return nil, errors.New("群配置游戏状态查询异常")
	}

	// 重新生成内联键盘回调key
	callbackDataKey, err := ButtonCallBackDataAddRedis(map[string]string{
		"chatGroupId": chatGroup.Id,
	})

	if err != nil {
		log.Println("内联键盘回调参数存入redis异常", err.Error())
		return nil, err
	}

	callbackDataQueryString := utils.MapToQueryString(map[string]string{
		"callbackDataKey": callbackDataKey,
	})

	inlineKeyboardButtons, err := buildGameplayConfigInlineKeyboardButton(chatGroup, callbackDataQueryString)

	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("chatGroupId %v 未查询到该群的配置信息 ", chatGroup.Id)
		return nil, err
	} else if err != nil {
		log.Printf("chatGroupId %v 该群的配置信息查询异常 %s", chatId, err.Error())
		return nil, err
	}

	newInlineKeyboardMarkup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🛠️当前玩法:【%s】", gameplayType.Name), fmt.Sprintf("%s%s", enums.CallbackGameplayType.Value, callbackDataQueryString)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🕹️开启状态: %s", gameplayStatus.Name), fmt.Sprintf("%s%s", enums.CallbackUpdateGameplayStatus.Value, callbackDataQueryString)),
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("⏲️开奖周期: %v 分钟", chatGroup.GameDrawCycle), fmt.Sprintf("%s%s", enums.CallbackUpdateGameDrawCycle.Value, callbackDataQueryString)),
		),
		inlineKeyboardButtons,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔍查询用户信息", fmt.Sprintf("%s%s", enums.CallbackQueryChatGroupUser.Value, callbackDataQueryString)),
			tgbotapi.NewInlineKeyboardButtonData("🖊️修改用户积分", fmt.Sprintf("%s%s", enums.CallbackUpdateChatGroupUserBalance.Value, callbackDataQueryString)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️返回", enums.CallbackAdminGroup.Value),
		),
	)
	return &newInlineKeyboardMarkup, nil
}
