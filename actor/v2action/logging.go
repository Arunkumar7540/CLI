package v2action

import (
	"context"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/cli/actor/actionerror"
	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	logcache "code.cloudfoundry.org/log-cache/pkg/client"
	"code.cloudfoundry.org/log-cache/pkg/rpc/logcache_v1"
	noaaErrors "github.com/cloudfoundry/noaa/errors"
	"github.com/cloudfoundry/sonde-go/events"
	log "github.com/sirupsen/logrus"
)

const (
	StagingLog      = "STG"
	RecentLogsLines = 100
)

var flushInterval = 300 * time.Millisecond

type LogMessage struct {
	message        string
	messageType    string
	timestamp      time.Time
	sourceType     string
	sourceInstance string
}

func (log LogMessage) Message() string {
	return log.message
}

func (log LogMessage) Type() string {
	return log.messageType
}

func (log LogMessage) Staging() bool {
	return log.sourceType == StagingLog
}

func (log LogMessage) Timestamp() time.Time {
	return log.timestamp
}

func (log LogMessage) SourceType() string {
	return log.sourceType
}

func (log LogMessage) SourceInstance() string {
	return log.sourceInstance
}

//TODO this is only used in tests
func NewLogMessage(message string, messageType int, timestamp time.Time, sourceType string, sourceInstance string) *LogMessage {
	return &LogMessage{
		message:        message,
		messageType:    events.LogMessage_MessageType_name[int32(events.LogMessage_MessageType(messageType))],
		timestamp:      timestamp,
		sourceType:     sourceType,
		sourceInstance: sourceInstance,
	}
}

type LogMessages []*LogMessage

func (lm LogMessages) Len() int { return len(lm) }

func (lm LogMessages) Less(i, j int) bool {
	return lm[i].timestamp.Before(lm[j].timestamp)
}

func (lm LogMessages) Swap(i, j int) {
	lm[i], lm[j] = lm[j], lm[i]
}

func (actor Actor) GetStreamingLogs(appGUID string, client LogCacheClient) (<-chan *LogMessage, <-chan error) {
	log.Info("Start Tailing Logs")

	// ready := actor.setOnConnectBlocker(client)

	// incomingLogStream, incomingErrStream := client.TailingLogs(appGUID, actor.Config.AccessToken())

	// outgoingLogStream, outgoingErrStream := actor.blockOnConnect(ready)

	// go actor.streamLogsBetween(incomingLogStream, incomingErrStream, outgoingLogStream, outgoingErrStream)

	outgoingLogStream := make(chan *LogMessage, 1000)
	outgoingErrStream := make(chan error, 1000)
	ctx, _ := context.WithCancel(context.Background())
	logcache.Walk(
		ctx,
		appGUID,
		logcache.Visitor(func(envelopes []*loggregator_v2.Envelope) bool {
			for _, e := range envelopes {
				if formatted, ok := filterAndFormat(e); ok {
					lw.Write(formatted)
				}
			}
			return true
		}),
		client.Read,
		logcache.WithWalkStartTime(time.Unix(0, walkStartTime)),
		logcache.WithWalkEnvelopeTypes(o.envelopeType),
		logcache.WithWalkBackoff(logcache.NewAlwaysRetryBackoff(250*time.Millisecond)),
		logcache.WithWalkNameFilter(o.nameFilter),
	)

	return outgoingLogStream, outgoingErrStream
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

	var logMessages []LogMessage
	for i := len(envelopes) - 1; i >= 0; i-- {
		envelope := envelopes[i]
		logEnvelope, ok := envelope.GetMessage().(*loggregator_v2.Envelope_Log)
		if !ok {
			continue
		}
		log := logEnvelope.Log

		logMessages = append(logMessages, LogMessage{
			message:        string(log.Payload),
			messageType:    loggregator_v2.Log_Type_name[int32(log.Type)],
			timestamp:      time.Unix(0, envelope.GetTimestamp()),
			sourceType:     envelope.GetTags()["source_type"], //TODO magical constant
			sourceInstance: envelope.GetInstanceId(),
		})
	}

	return logMessages, allWarnings, nil
}

func (actor Actor) GetStreamingLogsForApplicationByNameAndSpace(appName string, spaceGUID string, client NOAAClient) (<-chan *LogMessage, <-chan error, Warnings, error) {
	app, allWarnings, err := actor.GetApplicationByNameAndSpace(appName, spaceGUID)
	if err != nil {
		return nil, nil, allWarnings, err
	}

	messages, logErrs := actor.GetStreamingLogs(app.GUID, nil)

	return messages, logErrs, allWarnings, err
}

func (actor Actor) blockOnConnect(ready <-chan bool) (chan *LogMessage, chan error) {
	outgoingLogStream := make(chan *LogMessage)
	outgoingErrStream := make(chan error, 1)

	ticker := time.NewTicker(actor.Config.DialTimeout())

dance:
	for {
		select {
		case _, ok := <-ready:
			if !ok {
				break dance
			}
		case <-ticker.C:
			outgoingErrStream <- actionerror.NOAATimeoutError{}
			break dance
		}
	}

	return outgoingLogStream, outgoingErrStream
}

func (Actor) flushLogs(logs LogMessages, outgoingLogStream chan<- *LogMessage) LogMessages {
	sort.Stable(logs)
	for _, l := range logs {
		outgoingLogStream <- l
	}
	return LogMessages{}
}

func (Actor) setOnConnectBlocker(client NOAAClient) <-chan bool {
	ready := make(chan bool)
	var onlyRunOnInitialConnect sync.Once
	callOnConnectOrRetry := func() {
		onlyRunOnInitialConnect.Do(func() {
			close(ready)
		})
	}

	client.SetOnConnectCallback(callOnConnectOrRetry)

	return ready
}

func (actor Actor) streamLogsBetween(incomingLogStream <-chan *events.LogMessage, incomingErrStream <-chan error, outgoingLogStream chan<- *LogMessage, outgoingErrStream chan<- error) {
	log.Info("Processing Log Stream")

	defer close(outgoingLogStream)
	defer close(outgoingErrStream)

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	var logsToBeSorted LogMessages
	var eventClosed, errClosed bool

dance:
	for {
		select {
		case event, ok := <-incomingLogStream:
			if !ok {
				if !errClosed {
					log.Debug("logging event stream closed")
				}
				eventClosed = true
				break
			}

			logsToBeSorted = append(logsToBeSorted, &LogMessage{
				message:        string(event.GetMessage()),
				messageType:    events.LogMessage_MessageType_name[int32(event.GetMessageType())],
				timestamp:      time.Unix(0, event.GetTimestamp()),
				sourceInstance: event.GetSourceInstance(),
				sourceType:     event.GetSourceType(),
			})
		case err, ok := <-incomingErrStream:
			if !ok {
				if !errClosed {
					log.Debug("logging error stream closed")
				}
				errClosed = true
				break
			}

			if _, ok := err.(noaaErrors.RetryError); ok {
				break
			}

			if err != nil {
				outgoingErrStream <- err
			}
		case <-ticker.C:
			log.Debug("processing logsToBeSorted")
			logsToBeSorted = actor.flushLogs(logsToBeSorted, outgoingLogStream)
			if eventClosed && errClosed {
				log.Debug("stopping log processing")
				break dance
			}
		}
	}
}
