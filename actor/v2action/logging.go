package v2action

import (
	"code.cloudfoundry.org/cli/cf/errors"
	"context"
	"log"
	"strings"
	"time"

	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	logcache "code.cloudfoundry.org/log-cache/pkg/client"
	"code.cloudfoundry.org/log-cache/pkg/rpc/logcache_v1"
	"github.com/cloudfoundry/sonde-go/events"
	"github.com/sirupsen/logrus"
)

const (
	StagingLog      = "STG"
	RecentLogsLines = 100
)

//TODO publicize fields and remove getters
type LogMessage struct {
	message        string
	messageType    string
	timestamp      time.Time
	sourceType     string
	sourceInstance string
}

func (l LogMessage) Message() string {
	return l.message
}

func (l LogMessage) Type() string {
	return l.messageType
}

func (l LogMessage) Staging() bool {
	return l.sourceType == StagingLog
}

func (l LogMessage) Timestamp() time.Time {
	return l.timestamp
}

func (l LogMessage) SourceType() string {
	return l.sourceType
}

func (l LogMessage) SourceInstance() string {
	return l.sourceInstance
}

//TODO this is only used in tests
func NewLogMessage(message string, messageType int, timestamp time.Time, sourceType string, sourceInstance string) LogMessage {
	return LogMessage{
		message:        message,
		messageType:    events.LogMessage_MessageType_name[int32(events.LogMessage_MessageType(messageType))],
		timestamp:      timestamp,
		sourceType:     sourceType,
		sourceInstance: sourceInstance,
	}
}

type channelWriter struct {
	errChannel chan error
}

func (c channelWriter) Write(bytes []byte) (n int, err error) {
	c.errChannel <- errors.New(strings.Trim(string(bytes), "\n"))

	return len(bytes), nil
}

func (actor Actor) GetStreamingLogs(appGUID string, client LogCacheClient) (<-chan LogMessage, <-chan error, context.CancelFunc) {
	logrus.Info("Start Tailing Logs")

	outgoingLogStream := make(chan LogMessage, 1000)
	outgoingErrStream := make(chan error, 1000)
	ctx, cancelFunc := context.WithCancel(context.Background())
	go func() {
		defer close(outgoingLogStream)
		defer close(outgoingErrStream)

		logcache.Walk(
			ctx,
			appGUID,
			logcache.Visitor(func(envelopes []*loggregator_v2.Envelope) bool {
				logMessages := convertEnvelopesToLogMessages(envelopes)
				for _, logMessage := range logMessages {
					select {
					case <-ctx.Done():
						return false
					default:
						outgoingLogStream <- logMessage
					}
				}

				return true
			}),
			client.Read,
			logcache.WithWalkStartTime(time.Now().Add(-5*time.Second)),
			logcache.WithWalkEnvelopeTypes(logcache_v1.EnvelopeType_LOG),
			logcache.WithWalkBackoff(logcache.NewAlwaysRetryBackoff(250*time.Millisecond)),
			logcache.WithWalkLogger(log.New(channelWriter{
				errChannel: outgoingErrStream,
			}, "", 0)),
		)
	}()

	return outgoingLogStream, outgoingErrStream, cancelFunc
}

func (actor Actor) GetRecentLogsForApplicationByNameAndSpace(appName string, spaceGUID string, client LogCacheClient) ([]LogMessage, Warnings, error) {
	app, allWarnings, err := actor.GetApplicationByNameAndSpace(appName, spaceGUID)
	if err != nil {
		return nil, allWarnings, err
	}

	envelopes, err := client.Read(
		context.Background(),
		app.GUID,
		time.Time{},
		logcache.WithEnvelopeTypes(logcache_v1.EnvelopeType_LOG),
		logcache.WithLimit(RecentLogsLines),
		logcache.WithDescending(),
	)

	if err != nil {
		return nil, allWarnings, err
	}

	logMessages := convertEnvelopesToLogMessages(envelopes)
	var reorderedLogMessages []LogMessage
	for i := len(logMessages) - 1; i >= 0; i-- {
		reorderedLogMessages = append(reorderedLogMessages, logMessages[i])
	}

	return reorderedLogMessages, allWarnings, nil
}

func convertEnvelopesToLogMessages(envelopes []*loggregator_v2.Envelope) []LogMessage {
	var logMessages []LogMessage
	for _, envelope := range envelopes {
		logEnvelope, ok := envelope.GetMessage().(*loggregator_v2.Envelope_Log)
		if !ok {
			continue
		}
		log := logEnvelope.Log

		logMessages = append(logMessages, LogMessage{
			message:        string(log.Payload),
			messageType:    loggregator_v2.Log_Type_name[int32(log.Type)],
			timestamp:      time.Unix(0, envelope.GetTimestamp()),
			sourceType:     envelope.GetTags()["source_type"],
			sourceInstance: envelope.GetInstanceId(),
		})
	}
	return logMessages
}

func (actor Actor) GetStreamingLogsForApplicationByNameAndSpace(appName string, spaceGUID string, client LogCacheClient) (<-chan LogMessage, <-chan error, Warnings, error, context.CancelFunc) {
	app, allWarnings, err := actor.GetApplicationByNameAndSpace(appName, spaceGUID)
	if err != nil {
		return nil, nil, allWarnings, err, func() {}
	}

	messages, logErrs, cancel := actor.GetStreamingLogs(app.GUID, client)

	return messages, logErrs, allWarnings, err, cancel
}
