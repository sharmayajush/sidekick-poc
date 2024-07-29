package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/accuknox/kmux"
	kmuxConfig "github.com/accuknox/kmux/config"
	"github.com/open-policy-agent/opa/logging"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/kubearmor/sidekick/outputs"
	"github.com/kubearmor/sidekick/types"
)

var (
	nullClient          *outputs.Client
	slackClient         *outputs.Client
	cliqClient          *outputs.Client
	rocketchatClient    *outputs.Client
	mattermostClient    *outputs.Client
	teamsClient         *outputs.Client
	datadogClient       *outputs.Client
	discordClient       *outputs.Client
	alertmanagerClient  *outputs.Client
	elasticsearchClient *outputs.Client
	influxdbClient      *outputs.Client
	lokiClient          *outputs.Client
	natsClient          *outputs.Client
	stanClient          *outputs.Client
	awsClient           *outputs.Client
	smtpClient          *outputs.Client
	opsgenieClient      *outputs.Client
	webhookClient       *outputs.Client
	noderedClient       *outputs.Client
	cloudeventsClient   *outputs.Client
	azureClient         *outputs.Client
	gcpClient           *outputs.Client
	googleChatClient    *outputs.Client
	kafkaClient         *outputs.Client
	kafkaRestClient     *outputs.Client
	pagerdutyClient     *outputs.Client
	gcpCloudRunClient   *outputs.Client
	kubelessClient      *outputs.Client
	openfaasClient      *outputs.Client
	tektonClient        *outputs.Client
	webUIClient         *outputs.Client
	policyReportClient  *outputs.Client
	rabbitmqClient      *outputs.Client
	wavefrontClient     *outputs.Client
	fissionClient       *outputs.Client
	grafanaClient       *outputs.Client
	grafanaOnCallClient *outputs.Client
	yandexClient        *outputs.Client
	syslogClient        *outputs.Client
	mqttClient          *outputs.Client
	zincsearchClient    *outputs.Client
	gotifyClient        *outputs.Client
	spyderbatClient     *outputs.Client
	timescaleDBClient   *outputs.Client
	redisClient         *outputs.Client
	telegramClient      *outputs.Client
	n8nClient           *outputs.Client
	openObserveClient   *outputs.Client
	dynatraceClient     *outputs.Client

	statsdClient, dogstatsdClient *statsd.Client
	config                        *types.Configuration
	stats                         *types.Statistics
	promStats                     *types.PromStatistics
)

var globalMap map[string]*outputs.Client
var AlertLock *sync.RWMutex
var AlertBufferChannel chan []byte

type Alert struct {
	Id      int    `json:"id"`
	Message string `json:"message"`
}

type ChannelSlackSettings struct {
	ChannelsID  int      `gorm:"primaryKey;" json:"channels_id"`
	WebhookUrl  string   `gorm:"size:2000;not null;" json:"webhook_url"`
	SenderName  string   `gorm:"size:100;not null;" json:"sender_name"`
	ChannelName string   `gorm:"size:100;not null;" json:"channel_name"`
	Title       string   `gorm:"size:100;" json:"title"`
	TenantID    string   `gorm:"size:255;not null;" json:"tenant_id"`
	ChannelType string   `gorm:"not null;" json:"channel_type"`
	Channels    Channels `gorm:"foreignKey:ChannelsID"`
}

type Channels struct {
	ID              int       `gorm:"primaryKey;" json:"id"`
	ChannelTypeID   int       `gorm:"size:255;not null;" json:"channel_type_id"`
	ChannelType     string    `gorm:"not null;" json:"channel_type"`
	IntegrationName string    `gorm:"size:100;not null;unique" json:"integration_name"`
	Status          string    `gorm:"size:255;not null;" json:"status"`
	TenantID        string    `gorm:"size:255;not null;" json:"tenant_id"`
	CreatedBy       string    `gorm:"size:255;not null;" json:"created_by"`
	CreatedAt       time.Time `gorm:"size:50;not null;" json:"created_at"`
	UpdatedAt       time.Time `gorm:"size:50;not null;" json:"updated_at"`
}

var DB *gorm.DB

func main() {
	DB = GetConnection()
	var wg sync.WaitGroup
	wg.Add(1)
	outputs.AlertRunning = true
	outputs.AlertBufferChannel = make(chan []byte, 1000)

	go slackClient.WatchSlackAlerts()
	go ListenToKubearmorLiveLogs(&wg)
	go AddAlertFromBuffChan()

	wg.Wait()

}

func ListenToKubearmorLiveLogs(wg *sync.WaitGroup) {
	defer wg.Done()

	kubearmorTopic := "kubearmoralerts"
	streamSource := "rabbitmq"
	if streamSource == "rabbitmq" {
		err := kmux.Init(&kmuxConfig.Options{
			LocalConfigFile: "kmux-config-kubearmoralerts.yaml",
		})
		if err != nil {
			fmt.Printf("error while initializing kubearmor alerts configuration for kmux, error: %v", err)
			return
		}
	}
	kubearmorConsumer, err := kmux.NewStreamSource(kubearmorTopic)
	if err != nil {
		fmt.Printf("error in creating consumer . kubearmorTopic : %s | error : %s ", kubearmorTopic, err.Error())
	}
	// Connect with the source
	err = kubearmorConsumer.Connect()
	if err != nil {
		fmt.Printf("error in connecting to consumer . kubearmorTopic: %s | error : %s ", kubearmorTopic, err.Error())
	}

	defer kubearmorConsumer.Disconnect()

	// consume msg from channel
	for data := range kubearmorConsumer.Channel(context.Background()) {
		if data == nil {
			continue
		}
		err := fetchFromMysql(data, "Kubearmor")
		if err != nil {
			logging.Error().Msg("error in fetching data from mysql for kubearmor" + err.Error())
			continue
		}

		AlertBufferChannel <- data
	}
}

func fetchFromMysql(jsonFile []byte, componentName string) error {

	// DB.Table("cwpp.channels").

	var slackConfigs []ChannelSlackSettings
	if err := DB.Table("cwpp.channel_slack_settings").Select("cwpp.channel_slack_settings.webhook_url, cwpp.channel_slack_settings.channel_name, cwpp.channel_slack_settings.sender_name, cwpp.channel_slack_settings.title, cwpp.channels.tenant_id, cwpp.channels.channel_type").
		Joins("INNER JOIN cwpp.channels ON cwpp.channel_slack_settings.channels_id = cwpp.channels.id").
		Where("cwpp.channels.channel_type = slack AND cwpp.channels.status = ?", "Active").
		Scan(&slackConfigs).Error; err != nil {
		logging.Error().Msg("unable to get configurations for slack channel " + err.Error())
		return err
	}
	for _, slack := range slackConfigs {
		var err error
		globalMap[slack.TenantID+","+slack.ChannelType], err = outputs.NewClient(slack.ChannelType, slack.WebhookUrl, true, false, config, stats, promStats, statsdClient, dogstatsdClient)
		if err != nil {
			fmt.Println("error while creating a new client")
		} else {
			outputs.EnabledOutputs = append(outputs.EnabledOutputs, "Slack")
		}

		slackClient.SlackPost(types.KubearmorPayload{
			Timestamp:    1721783201,
			Hostname:     "accuknox",
			EventType:    "Alert",
			OutputFields: jsonFile,
		})
	}

	//
	// var err error
	// slackClient, err = outputs.NewClient("Slack", "https://hooks.slack.com/services/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", true, false, config, stats, promStats, statsdClient, dogstatsdClient)
	// if err != nil {
	// 	fmt.Println("error while creating a new client")
	// } else {
	// 	outputs.EnabledOutputs = append(outputs.EnabledOutputs, "Slack")
	// }
	// var output map[string]interface{}
	// p := Alert{
	// 	Id:      30,
	// 	Message: "abc",
	// }

	// jsonData, err := json.Marshal(p)
	// json.Unmarshal(jsonData, output)

	// slackClient.SlackPost(types.KubearmorPayload{
	// 	Timestamp:    1721783201,
	// 	Hostname:     "accuknox",
	// 	EventType:    "Alert",
	// 	OutputFields: output,
	// })
	return nil
}

func AddAlertFromBuffChan() {
	for outputs.AlertRunning {
		select {
		case res := <-AlertBufferChannel:

			alert := Alert{}

			json.Unmarshal(res, alert)

			AlertLock.RLock()
			for uid := range outputs.AlertStructs {
				select {
				case outputs.AlertStructs[uid].Broadcast <- (alert):
				default:
				}
			}
			AlertLock.RUnlock()

		default:
			time.Sleep(time.Millisecond * 10)
		}

	}
}

const (
	host     = "localhost"
	port     = 5432
	user     = "postgres"
	password = "test123"
	dbname   = "accuknox"
)

func GetConnection() *gorm.DB {

	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s "+
		"password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)

	db, err := gorm.Open(postgres.Open(psqlInfo), &gorm.Config{})

	if err != nil {
		panic("failed to connect database")
	}

	log.Println("DB Connection established...")

	return db
}
