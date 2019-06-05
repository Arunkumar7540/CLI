package v2action_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/cli/actor/actionerror"
	. "code.cloudfoundry.org/cli/actor/v2action"
	"code.cloudfoundry.org/cli/actor/v2action/v2actionfakes"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv2"
	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	logcache "code.cloudfoundry.org/log-cache/pkg/client"
	noaaErrors "github.com/cloudfoundry/noaa/errors"
	"github.com/cloudfoundry/sonde-go/events"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Logging Actions", func() {
	var (
		actor                     *Actor
		fakeCloudControllerClient *v2actionfakes.FakeCloudControllerClient
		fakeConfig                *v2actionfakes.FakeConfig
		fakeNOAAClient            *v2actionfakes.FakeNOAAClient
		fakeLogCacheClient        *v2actionfakes.FakeLogCacheClient
	)

	BeforeEach(func() {
		actor, fakeCloudControllerClient, _, fakeConfig = NewTestActor()
		fakeNOAAClient = new(v2actionfakes.FakeNOAAClient)
		fakeLogCacheClient = new(v2actionfakes.FakeLogCacheClient)
		fakeConfig.AccessTokenReturns("AccessTokenForTest")
	})

	Describe("LogMessage", func() {
		Describe("Staging", func() {
			When("the log is a staging log", func() {
				It("returns true", func() {
					message := NewLogMessage("", 0, time.Now(), "STG", "")
					Expect(message.Staging()).To(BeTrue())
				})
			})

			When("the log is any other kind of log", func() {
				It("returns false", func() {
					message := NewLogMessage("", 0, time.Now(), "APP", "")
					Expect(message.Staging()).To(BeFalse())
				})
			})
		})
	})

	Describe("GetStreamingLogs", func() {
		var (
			expectedAppGUID string

			messages <-chan *LogMessage
			errs     <-chan error
		)

		BeforeEach(func() {
			expectedAppGUID = "some-app-guid"
		})

		AfterEach(func() {
			Eventually(messages).Should(BeClosed())
			Eventually(errs).Should(BeClosed())
		})

		JustBeforeEach(func() {
			messages, errs = actor.GetStreamingLogs(expectedAppGUID, fakeLogCacheClient)
		})

		FWhen("receiving logs", func() {
			BeforeEach(func() {
				fakeConfig.DialTimeoutReturns(60 * time.Minute)
				fakeLogCacheClient.ReadStub = func(
					ctx context.Context,
					sourceID string,
					start time.Time,
					opts ...logcache.ReadOption,
				) ([]*loggregator_v2.Envelope, error) {
					var fullEnvelopeSet []*loggregator_v2.Envelope
					for i := 0; i < 100; i++ {
						fullEnvelopeSet = append(fullEnvelopeSet, &loggregator_v2.Envelope{
							Timestamp:  time.Now().UnixNano(),
							SourceId:   "some-app-guid",
							InstanceId: "some-source-instance",
							Message: &loggregator_v2.Envelope_Log{
								Log: &loggregator_v2.Log{
									Payload: []byte(fmt.Sprintf("message-%d", i)),
									Type:    loggregator_v2.Log_OUT,
								},
							},
							Tags: map[string]string{
								"source_type": "some-source-type",
							},
						})
					}

					if fakeLogCacheClient.ReadCallCount() > 1 {
						return fullEnvelopeSet[:50], nil
					}
					return fullEnvelopeSet, nil
				}
			})

			It("converts them to log messages, sorts them, and passes them through the messages channel", func() {
				Eventually(messages).Should(HaveLen(150))
				Expect(errs).ToNot(Receive())
			})
		})

		When("receiving errors", func() {
			var (
				err1 error
				err2 error

				waiting chan bool
			)

			Describe("nil error", func() {
				BeforeEach(func() {
					fakeConfig.DialTimeoutReturns(time.Minute)

					waiting = make(chan bool)
					fakeNOAAClient.TailingLogsStub = func(_ string, _ string) (<-chan *events.LogMessage, <-chan error) {
						eventStream := make(chan *events.LogMessage)
						errStream := make(chan error, 1)

						Expect(fakeNOAAClient.SetOnConnectCallbackCallCount()).To(Equal(1))
						onConnectOrOnRetry := fakeNOAAClient.SetOnConnectCallbackArgsForCall(0)

						go func() {
							defer close(eventStream)
							defer close(errStream)
							onConnectOrOnRetry()

							errStream <- nil
							close(waiting)
						}()

						return eventStream, errStream
					}
				})

				It("does not pass the nil along", func() {
					Eventually(waiting).Should(BeClosed())
					Consistently(errs).ShouldNot(Receive())
				})
			})

			Describe("unexpected error", func() {
				BeforeEach(func() {
					fakeConfig.DialTimeoutReturns(time.Microsecond) // tests don't care about this timeout, ignore it

					err1 = errors.New("ZOMG")
					err2 = errors.New("Fiddlesticks")

					fakeNOAAClient.TailingLogsStub = func(_ string, _ string) (<-chan *events.LogMessage, <-chan error) {
						eventStream := make(chan *events.LogMessage)
						errStream := make(chan error, 1)

						go func() {
							defer close(eventStream)
							defer close(errStream)

							errStream <- err1
							errStream <- err2
						}()

						return eventStream, errStream
					}
				})

				It("passes them through the errors channel", func() {
					Eventually(errs).Should(Receive(Equal(err1)))
					Eventually(errs).Should(Receive(Equal(err2)))
				})
			})

			Describe("NOAA's RetryError", func() {
				When("NOAA is able to recover", func() {
					BeforeEach(func() {
						fakeConfig.DialTimeoutReturns(60 * time.Minute)

						fakeNOAAClient.TailingLogsStub = func(_ string, _ string) (<-chan *events.LogMessage, <-chan error) {
							eventStream := make(chan *events.LogMessage)
							errStream := make(chan error, 1)

							Expect(fakeNOAAClient.SetOnConnectCallbackCallCount()).To(Equal(1))
							onConnectOrOnRetry := fakeNOAAClient.SetOnConnectCallbackArgsForCall(0)

							go func() {
								defer close(eventStream)
								defer close(errStream)

								// can be called multiple times. Should be resilient to that
								onConnectOrOnRetry()
								errStream <- noaaErrors.NewRetryError(errors.New("error 1"))
								onConnectOrOnRetry()

								outMessage := events.LogMessage_OUT
								ts1 := int64(10)
								sourceType := "some-source-type"
								sourceInstance := "some-source-instance"

								eventStream <- &events.LogMessage{
									Message:        []byte("message-1"),
									MessageType:    &outMessage,
									Timestamp:      &ts1,
									SourceType:     &sourceType,
									SourceInstance: &sourceInstance,
								}
							}()

							return eventStream, errStream
						}
					})

					It("continues without issue", func() {
						Eventually(messages).Should(Receive())
						Consistently(errs).ShouldNot(Receive())
					})
				})

				When("NOAA has trouble connecting", func() {
					BeforeEach(func() {
						fakeConfig.DialTimeoutReturns(time.Microsecond)
						fakeNOAAClient.TailingLogsStub = func(_ string, _ string) (<-chan *events.LogMessage, <-chan error) {
							eventStream := make(chan *events.LogMessage)
							errStream := make(chan error, 1)

							go func() {
								defer close(eventStream)
								defer close(errStream)

								// explicitly skip the on call to simulate ready never being triggered

								errStream <- noaaErrors.NewRetryError(errors.New("error 1"))

								outMessage := events.LogMessage_OUT
								ts1 := int64(10)
								sourceType := "some-source-type"
								sourceInstance := "some-source-instance"

								eventStream <- &events.LogMessage{
									Message:        []byte("message-1"),
									MessageType:    &outMessage,
									Timestamp:      &ts1,
									SourceType:     &sourceType,
									SourceInstance: &sourceInstance,
								}
							}()

							return eventStream, errStream
						}
					})

					It("returns a NOAATimeoutError and continues", func() {
						Eventually(errs).Should(Receive(MatchError(actionerror.NOAATimeoutError{})))
						Eventually(messages).Should(Receive())

						Expect(fakeConfig.DialTimeoutCallCount()).To(Equal(1))
					})
				})
			})
		})
	})

	Describe("GetRecentLogsForApplicationByNameAndSpace", func() {
		When("the application can be found", func() {
			BeforeEach(func() {
				fakeCloudControllerClient.GetApplicationsReturns(
					[]ccv2.Application{
						{
							Name: "some-app",
							GUID: "some-app-guid",
						},
					},
					ccv2.Warnings{"some-app-warnings"},
					nil,
				)
			})

			When("Log Cache returns logs", func() {
				BeforeEach(func() {
					messages := []*loggregator_v2.Envelope{
						{
							Timestamp:  int64(20),
							SourceId:   "some-app-guid",
							InstanceId: "some-source-instance",
							Message: &loggregator_v2.Envelope_Log{
								Log: &loggregator_v2.Log{
									Payload: []byte("message-2"),
									Type:    loggregator_v2.Log_OUT,
								},
							},
							Tags: map[string]string{
								"source_type": "some-source-type",
							},
						},
						{
							Timestamp:  int64(10),
							SourceId:   "some-app-guid",
							InstanceId: "some-source-instance",
							Message: &loggregator_v2.Envelope_Log{
								Log: &loggregator_v2.Log{
									Payload: []byte("message-1"),
									Type:    loggregator_v2.Log_OUT,
								},
							},
							Tags: map[string]string{
								"source_type": "some-source-type",
							},
						},
					}

					fakeLogCacheClient.ReadReturns(messages, nil)
				})

				It("returns all the recent logs and warnings", func() {
					messages, warnings, err := actor.GetRecentLogsForApplicationByNameAndSpace("some-app", "some-space-guid", fakeLogCacheClient)
					Expect(err).ToNot(HaveOccurred())
					Expect(warnings).To(ConsistOf("some-app-warnings"))

					Expect(messages[0].Message()).To(Equal("message-1"))
					Expect(messages[0].Type()).To(Equal("OUT"))
					Expect(messages[0].Timestamp()).To(Equal(time.Unix(0, 10)))
					Expect(messages[0].SourceType()).To(Equal("some-source-type"))
					Expect(messages[0].SourceInstance()).To(Equal("some-source-instance"))

					Expect(messages[1].Message()).To(Equal("message-2"))
					Expect(messages[1].Type()).To(Equal("OUT"))
					Expect(messages[1].Timestamp()).To(Equal(time.Unix(0, 20)))
					Expect(messages[1].SourceType()).To(Equal("some-source-type"))
					Expect(messages[1].SourceInstance()).To(Equal("some-source-instance"))
				})
			})

			When("Log Cache returns non-log envelopes", func() {
				BeforeEach(func() {
					messages := []*loggregator_v2.Envelope{
						{
							Timestamp:  int64(10),
							SourceId:   "some-app-guid",
							InstanceId: "some-source-instance",
							Message:    &loggregator_v2.Envelope_Counter{},
							Tags: map[string]string{
								"source_type": "some-source-type",
							},
						},
					}

					fakeLogCacheClient.ReadReturns(messages, nil)
				})

				It("ignores them", func() {
					messages, _, err := actor.GetRecentLogsForApplicationByNameAndSpace("some-app", "some-space-guid", fakeLogCacheClient)
					Expect(err).ToNot(HaveOccurred())
					Expect(messages).To(BeEmpty())
				})
			})

			When("Log Cache errors", func() {
				var expectedErr error

				BeforeEach(func() {
					expectedErr = errors.New("ZOMG")
					fakeLogCacheClient.ReadReturns(nil, expectedErr)
				})

				It("returns error and warnings", func() {
					_, warnings, err := actor.GetRecentLogsForApplicationByNameAndSpace("some-app", "some-space-guid", fakeLogCacheClient)
					Expect(err).To(MatchError(expectedErr))
					Expect(warnings).To(ConsistOf("some-app-warnings"))
				})
			})
		})

		When("finding the application errors", func() {
			var expectedErr error

			BeforeEach(func() {
				expectedErr = errors.New("ZOMG")
				fakeCloudControllerClient.GetApplicationsReturns(
					nil,
					ccv2.Warnings{"some-app-warnings"},
					expectedErr,
				)
			})

			It("returns error and warnings", func() {
				_, warnings, err := actor.GetRecentLogsForApplicationByNameAndSpace("some-app", "some-space-guid", fakeLogCacheClient)
				Expect(err).To(MatchError(expectedErr))
				Expect(warnings).To(ConsistOf("some-app-warnings"))

				Expect(fakeNOAAClient.RecentLogsCallCount()).To(Equal(0))
			})
		})
	})

	Describe("GetStreamingLogsForApplicationByNameAndSpace", func() {
		When("the application can be found", func() {
			var (
				expectedAppGUID string

				messages <-chan *LogMessage
				logErrs  <-chan error
			)

			AfterEach(func() {
				Eventually(messages).Should(BeClosed())
				Eventually(logErrs).Should(BeClosed())
			})

			BeforeEach(func() {
				expectedAppGUID = "some-app-guid"

				fakeCloudControllerClient.GetApplicationsReturns(
					[]ccv2.Application{
						{
							Name: "some-app",
							GUID: expectedAppGUID,
						},
					},
					ccv2.Warnings{"some-app-warnings"},
					nil,
				)

				fakeConfig.DialTimeoutReturns(60 * time.Minute)

				fakeNOAAClient.TailingLogsStub = func(appGUID string, authToken string) (<-chan *events.LogMessage, <-chan error) {
					Expect(appGUID).To(Equal(expectedAppGUID))
					Expect(authToken).To(Equal("AccessTokenForTest"))

					Expect(fakeNOAAClient.SetOnConnectCallbackCallCount()).To(Equal(1))
					onConnectOrOnRetry := fakeNOAAClient.SetOnConnectCallbackArgsForCall(0)

					eventStream := make(chan *events.LogMessage)
					errStream := make(chan error, 1)

					go func() {
						defer close(eventStream)
						defer close(errStream)

						onConnectOrOnRetry()

						outMessage := events.LogMessage_OUT
						ts1 := int64(10)
						sourceType := "some-source-type"
						sourceInstance := "some-source-instance"

						eventStream <- &events.LogMessage{
							Message:        []byte("message-1"),
							MessageType:    &outMessage,
							Timestamp:      &ts1,
							SourceType:     &sourceType,
							SourceInstance: &sourceInstance,
						}

						errMessage := events.LogMessage_ERR
						ts2 := int64(20)

						eventStream <- &events.LogMessage{
							Message:        []byte("message-2"),
							MessageType:    &errMessage,
							Timestamp:      &ts2,
							SourceType:     &sourceType,
							SourceInstance: &sourceInstance,
						}
					}()

					return eventStream, errStream
				}
			})

			It("converts them to log messages and passes them through the messages channel", func() {
				var err error
				var warnings Warnings
				messages, logErrs, warnings, err = actor.GetStreamingLogsForApplicationByNameAndSpace("some-app", "some-space-guid", fakeNOAAClient)

				Expect(err).ToNot(HaveOccurred())
				Expect(warnings).To(ConsistOf("some-app-warnings"))

				message := <-messages
				Expect(message.Message()).To(Equal("message-1"))
				Expect(message.Type()).To(Equal("OUT"))
				Expect(message.Timestamp()).To(Equal(time.Unix(0, 10)))
				Expect(message.SourceType()).To(Equal("some-source-type"))
				Expect(message.SourceInstance()).To(Equal("some-source-instance"))

				message = <-messages
				Expect(message.Message()).To(Equal("message-2"))
				Expect(message.Type()).To(Equal("ERR"))
				Expect(message.Timestamp()).To(Equal(time.Unix(0, 20)))
				Expect(message.SourceType()).To(Equal("some-source-type"))
				Expect(message.SourceInstance()).To(Equal("some-source-instance"))
			})
		})

		When("finding the application errors", func() {
			var expectedErr error

			BeforeEach(func() {
				expectedErr = errors.New("ZOMG")
				fakeCloudControllerClient.GetApplicationsReturns(
					nil,
					ccv2.Warnings{"some-app-warnings"},
					expectedErr,
				)
			})

			It("returns error and warnings", func() {
				_, _, warnings, err := actor.GetStreamingLogsForApplicationByNameAndSpace("some-app", "some-space-guid", fakeNOAAClient)
				Expect(err).To(MatchError(expectedErr))
				Expect(warnings).To(ConsistOf("some-app-warnings"))

				Expect(fakeNOAAClient.TailingLogsCallCount()).To(Equal(0))
			})
		})
	})
})
