// Copyright 2024 The Cloud Native Events Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package restapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	types2 "github.com/cloudevents/sdk-go/v2/types"
	"github.com/google/uuid"

	restapi "github.com/redhat-cne/rest-api/v2"
	"github.com/redhat-cne/sdk-go/pkg/channel"
	"github.com/redhat-cne/sdk-go/pkg/event"
	"github.com/redhat-cne/sdk-go/pkg/event/ptp"
	"github.com/redhat-cne/sdk-go/pkg/pubsub"
	"github.com/redhat-cne/sdk-go/pkg/types"
	v1event "github.com/redhat-cne/sdk-go/v1/event"
	api "github.com/redhat-cne/sdk-go/v1/pubsub"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

var (
	server *restapi.Server

	eventOutCh       chan *channel.DataChan
	closeCh          chan struct{}
	wg               sync.WaitGroup
	port             = 8990
	apHost           = "localhost"
	apPath           = "/api/ocloudNotifications/v2/"
	resource         = "/east-edge-10/Node3/sync/sync-status/sync-state"
	resourceInvalid  = "/east-edge-10/Node3/invalid"
	storePath        = "."
	ObjSub           pubsub.PubSub
	ObjPub           pubsub.PubSub
	testSource       = "/sync/sync-status/sync-state"
	testType         = "event.synchronization-state-change"
	endpoint         = "http://localhost:8990//api/ocloudNotifications/v2/dummy"
	onceCloseEvent   sync.Once
	onceCloseCloseCh sync.Once
)

func onReceiveOverrideFn(e cloudevents.Event, d *channel.DataChan) error {
	if e.Source() != resource {
		return fmt.Errorf("could not find any events for requested resource type %s", e.Source())
	}

	data := &event.Data{
		Version: event.APISchemaVersion,
		Values:  []event.DataValue{{Resource: resource, DataType: event.NOTIFICATION, ValueType: event.ENUMERATION, Value: ptp.FREERUN}},
	}
	ce := cloudevents.NewEvent(cloudevents.VersionV1)
	ce.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
	ce.SetType(testType)
	ce.SetSource(testSource)
	ce.SetSpecVersion(cloudevents.VersionV1)
	ce.SetID(uuid.New().String())
	ce.SetData("", *data) //nolint:errcheck
	d.Data = &ce

	return nil
}

func init() {
	eventOutCh = make(chan *channel.DataChan, 10)
	closeCh = make(chan struct{})
}

func TestMain(m *testing.M) {
	server = restapi.InitServer(port, apHost, apPath, storePath, eventOutCh, closeCh, onReceiveOverrideFn)
	//start http server
	server.Start()

	wg.Add(1)
	go func() {
		for d := range eventOutCh {
			if d.Type == channel.STATUS && d.StatusChan != nil {
				clientID, _ := uuid.Parse("123e4567-e89b-12d3-a456-426614174001")
				cneEvent := v1event.CloudNativeEvent()
				cneEvent.SetID(ObjPub.ID)
				cneEvent.Type = string(ptp.PtpStateChange)
				cneEvent.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
				cneEvent.SetDataContentType(event.ApplicationJSON)
				data := event.Data{
					Version: "v1.0",
					Values: []event.DataValue{{
						Resource:  resource,
						DataType:  event.NOTIFICATION,
						ValueType: event.ENUMERATION,
						Value:     ptp.ACQUIRING_SYNC,
					},
					},
				}
				data.SetVersion("1.0") //nolint:errcheck
				cneEvent.SetData(data)
				e := cloudevents.Event{
					Context: cloudevents.EventContextV1{
						Type:       string(ptp.PtpStateChange),
						Source:     cloudevents.URIRef{URL: url.URL{Scheme: "http", Host: "example.com", Path: "/source"}},
						ID:         "status event",
						Time:       &cloudevents.Timestamp{Time: time.Date(2020, 03, 21, 12, 34, 56, 780000000, time.UTC)},
						DataSchema: &types2.URI{URL: url.URL{Scheme: "http", Host: "example.com", Path: "/schema"}},
						Subject:    func(s string) *string { return &s }("topic"),
					}.AsV1(),
				}
				_ = e.SetData("", cneEvent)
				func() {
					defer func() {
						if err := recover(); err != nil {
							log.Errorf("error on close channel")
						}
					}()
					if d.Address == resourceInvalid {
						d.StatusChan <- &channel.StatusChan{
							ID:         "123",
							ClientID:   clientID,
							Data:       &e,
							StatusCode: http.StatusBadRequest,
							Message:    []byte("Client not subscribed"),
						}
					} else {
						d.StatusChan <- &channel.StatusChan{
							ID:         "123",
							ClientID:   clientID,
							Data:       &e,
							StatusCode: http.StatusOK,
							Message:    []byte("ok"),
						}
					}
				}()
			}
			log.Infof("incoming data %#v", d)
		}
	}()
	time.Sleep(2 * time.Second)
	port = server.Port()
	exitVal := m.Run()
	os.Exit(exitVal)
}

func TestServer_Health(t *testing.T) {
	// CHECK URL IS UP
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "health"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.1 Create a subscription resource
// 5.3.1.5 (2) Expected Results:
// The return code is “400 Bad request”, without message body,
// when the subscription request is not correct.
func TestServer_CreateSubscription_KO_ReqWithoutMsgBody(t *testing.T) {
	/// create new subscription without message body
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), bytes.NewBuffer(nil))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	assert.Empty(t, bodyBytes)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestServer_CreateSubscription_KO_invalidEndpoint(t *testing.T) {
	// create subscription
	subBadData := map[string]interface{}{
		"boo":             endpoint,
		"ResourceAddress": resource,
	}

	data, err := json.Marshal(&subBadData)
	assert.Nil(t, err)
	assert.NotNil(t, data)
	/// create new subscription
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), bytes.NewBuffer(data))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	assert.Empty(t, bodyBytes)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestServer_CreateSubscription_KO_invalidResource(t *testing.T) {
	// create subscription
	subBadData := map[string]interface{}{
		"EndpointUri": endpoint,
		"boo":         resource,
	}

	data, err := json.Marshal(&subBadData)
	assert.Nil(t, err)
	assert.NotNil(t, data)
	/// create new subscription
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), bytes.NewBuffer(data))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	assert.Empty(t, bodyBytes)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.1 Create a subscription resource
// 5.3.1.5 (3) Expected Results:
// The return code is “404 Not found”, without message body, when the subscription resource is not available.
func TestServer_CreateSubscription_KO_ResourceNotAvail(t *testing.T) {
	// create subscription
	sub := api.NewPubSub(
		&types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy")}},
		"resourceNotExist", "2.0")
	data, err := json.Marshal(&sub)
	assert.Nil(t, err)
	assert.NotNil(t, data)
	/// create new subscription
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), bytes.NewBuffer(data))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	assert.Empty(t, bodyBytes)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.1 Create a subscription resource
// 5.3.1.5 (1) Expected Results:
// The return code is “201 OK”, with Response message body content that contains a Subscriptioninfo,
// when the subscription request is correct and processed by the EP.
func TestServer_CreateSubscription_OK(t *testing.T) {
	// create subscription
	sub := api.NewPubSub(
		&types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy")}},
		resource, "2.0")
	data, err := json.Marshal(&sub)
	assert.Nil(t, err)
	assert.NotNil(t, data)
	/// create new subscription
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), bytes.NewBuffer(data))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	bodyString := string(bodyBytes)
	log.Print(bodyString)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	err = json.Unmarshal(bodyBytes, &ObjSub)
	assert.Nil(t, err)
	assert.NotEmpty(t, ObjSub.ID)
	assert.NotEmpty(t, ObjSub.URILocation)
	assert.NotEmpty(t, ObjSub.EndPointURI)
	assert.NotEmpty(t, ObjSub.Resource)
	assert.Equal(t, sub.Resource, ObjSub.Resource)
	log.Infof("Subscription:\n%s", ObjSub.String())
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.1 Create a subscription resource
// 5.3.1.5 (4) Expected Results:
// The return code is “409 Conflict”, without message body, when the subscription resource already exists.
func TestServer_CreateSubscription_KO_SubAlreadyExist(t *testing.T) {
	// create subscription
	sub := api.NewPubSub(
		&types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy")}},
		resource, "2.0")
	data, err := json.Marshal(&sub)
	assert.Nil(t, err)
	assert.NotNil(t, data)
	/// create new subscription
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), bytes.NewBuffer(data))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	assert.Empty(t, bodyBytes)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

// multiple clients should be able to create subscriptions with
// the same resource but different endpointURI
func TestServer_CreateSubscription_MultiClients(t *testing.T) {
	// create subscription
	sub := api.NewPubSub(
		&types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy2")}},
		resource, "2.0")
	data, err := json.Marshal(&sub)
	assert.Nil(t, err)
	assert.NotNil(t, data)
	/// create new subscription
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), bytes.NewBuffer(data))
	assert.Nil(t, err)

	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	bodyString := string(bodyBytes)
	log.Print(bodyString)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	err = json.Unmarshal(bodyBytes, &ObjSub)
	assert.Nil(t, err)
	assert.NotEmpty(t, ObjSub.ID)
	assert.NotEmpty(t, ObjSub.URILocation)
	assert.NotEmpty(t, ObjSub.EndPointURI)
	assert.NotEmpty(t, ObjSub.Resource)
	assert.Equal(t, sub.Resource, ObjSub.Resource)
	log.Infof("Subscription:\n%s", ObjSub.String())
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.2 Get a list of subscription resources
// 5.3.2.5 (1) Expected Results:
// The return code is “200 OK”, with Response message body content containing an array of Subscriptioninfo.
func TestServer_ListSubscriptions(t *testing.T) {
	// Get All Subscriptions
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close() // Close body only if response non-nil
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var subList []pubsub.PubSub
	log.Printf("TestServer_ListSubscriptions :%s\n", string(bodyBytes))
	err = json.Unmarshal(bodyBytes, &subList)
	assert.Nil(t, err)
	assert.Greater(t, len(subList), 0)
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.3 Get Detail of individual subscription resource
// 5.3.3.5 (1) Expected Results:
// The return code is “200 OK”, with Response message body content containing a Subscriptioninfo.
func TestServer_GetSubscription_OK(t *testing.T) {
	// Get Just Created Subscription
	req, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "subscriptions", ObjSub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	rSub := api.New()
	err = json.Unmarshal(bodyBytes, &rSub)
	if e, ok := err.(*json.SyntaxError); ok {
		log.Infof("syntax error at byte offset %d", e.Offset)
	}
	assert.Nil(t, err)
	assert.Equal(t, rSub.ID, ObjSub.ID)
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.3 Get Detail of individual subscription resource
// 5.3.3.5 (2) Expected Results:
// The return code is “404 Not found”, without message body, when the subscription resource is not available (not created).
func TestServer_GetSubscription_KO_SubNotAvail(t *testing.T) {
	// Get Just Created Subscription
	req, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "subscriptions", "InvalidId"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	assert.Empty(t, bodyBytes)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_CreatePublisher(t *testing.T) {
	pub := pubsub.PubSub{
		ID:          "",
		EndPointURI: &types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy")}},
		Resource:    resource,
	}
	pubData, err := json.Marshal(&pub)
	assert.Nil(t, err)
	assert.NotNil(t, pubData)
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "publishers"), bytes.NewBuffer(pubData))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	pubBodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	err = json.Unmarshal(pubBodyBytes, &ObjPub)
	assert.Nil(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.NotEmpty(t, ObjPub.ID)
	assert.NotEmpty(t, ObjPub.URILocation)
	assert.NotEmpty(t, ObjPub.EndPointURI)
	assert.NotEmpty(t, ObjPub.Resource)
	assert.Equal(t, pub.Resource, ObjPub.Resource)
	log.Infof("publisher \n%s", ObjPub.String())
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.6 Event pull status notification
// 5.3.6.5 (1) Expected results: The return code is “200 OK”.
func TestServer_GetCurrentState_OK(t *testing.T) {
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, ObjSub.Resource, "CurrentState"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	s, err2 := io.ReadAll(resp.Body)
	assert.Nil(t, err2)
	log.Infof("tedt %s ", string(s))
	var e cloudevents.Event
	err = json.Unmarshal(s, &e)
	assert.Nil(t, err)
	assert.Equal(t, testSource, e.Context.GetSource())
	assert.Equal(t, testType, e.Context.GetType())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.6 Event pull status notification
// 5.3.6.5 (2) Expected results:
// The return code is “404 Not Found”, when event notification resource is not available on this node.
func TestServer_GetCurrentState_KO_ResourceInvalid(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// try getting event
	time.Sleep(2 * time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, resourceInvalid, "CurrentState"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	s, err2 := io.ReadAll(resp.Body)
	assert.Nil(t, err2)
	log.Infof("tedt %s ", string(s))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func onReceiveOverrideFnEmptyEventData(e cloudevents.Event, d *channel.DataChan) error {
	if e.Source() != resource {
		return fmt.Errorf("could not find any events for requested resource type %s", e.Source())
	}

	ce := cloudevents.NewEvent(cloudevents.VersionV1)
	ce.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
	ce.SetType(testType)
	ce.SetSource(testSource)
	ce.SetSpecVersion(cloudevents.VersionV1)
	ce.SetID(uuid.New().String())
	ce.SetData("", nil) //nolint:errcheck
	d.Data = &ce

	return nil
}

func TestServer_GetCurrentState_KO_EmptyEventData(t *testing.T) {
	server.SetOnStatusReceiveOverrideFn(onReceiveOverrideFnEmptyEventData)
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, ObjSub.Resource, "CurrentState"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	s, err2 := io.ReadAll(resp.Body)
	assert.Nil(t, err2)
	log.Infof("tedt %s ", string(s))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func onReceiveOverrideFnInvalidEventData(e cloudevents.Event, d *channel.DataChan) error {
	if e.Source() != resource {
		return fmt.Errorf("could not find any events for requested resource type %s", e.Source())
	}

	data := &event.Data{
		Version: event.APISchemaVersion,
		Values:  []event.DataValue{},
	}
	ce := cloudevents.NewEvent(cloudevents.VersionV1)
	ce.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
	ce.SetType(testType)
	ce.SetSource(testSource)
	ce.SetSpecVersion(cloudevents.VersionV1)
	ce.SetID(uuid.New().String())
	ce.SetData("", *data) //nolint:errcheck
	d.Data = &ce

	return nil
}

func TestServer_GetCurrentState_KO_InvalidEventData(t *testing.T) {
	server.SetOnStatusReceiveOverrideFn(onReceiveOverrideFnInvalidEventData)
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, ObjSub.Resource, "CurrentState"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	s, err2 := io.ReadAll(resp.Body)
	assert.Nil(t, err2)
	log.Infof("tedt %s ", string(s))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func onReceiveOverrideFnEventNotFound(e cloudevents.Event, d *channel.DataChan) error {
	if e.Source() != resource {
		return fmt.Errorf("could not find any events for requested resource type %s", e.Source())
	}

	data := &event.Data{
		Version: event.APISchemaVersion,
		Values:  []event.DataValue{{Resource: "event-not-found", DataType: event.NOTIFICATION, ValueType: event.ENUMERATION, Value: ptp.FREERUN}},
	}
	ce := cloudevents.NewEvent(cloudevents.VersionV1)
	ce.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
	ce.SetType(testType)
	ce.SetSource(testSource)
	ce.SetSpecVersion(cloudevents.VersionV1)
	ce.SetID(uuid.New().String())
	ce.SetData("", *data) //nolint:errcheck
	d.Data = &ce

	return nil
}
func TestServer_GetCurrentState_KO_event_not_found(t *testing.T) {
	server.SetOnStatusReceiveOverrideFn(onReceiveOverrideFnEventNotFound)
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, ObjSub.Resource, "CurrentState"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	s, err2 := io.ReadAll(resp.Body)
	assert.Nil(t, err2)
	log.Infof("tedt %s ", string(s))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_GetPublisher(t *testing.T) {
	// Get Just created Publisher
	req, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "publishers", ObjPub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	pubBodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	var rPub pubsub.PubSub
	log.Printf("the data %s", string(pubBodyBytes))
	err = json.Unmarshal(pubBodyBytes, &rPub)
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	assert.Nil(t, err)
	assert.Equal(t, ObjPub.ID, rPub.ID)
}

func TestServer_ListPublishers(t *testing.T) {
	// Get All Publisher
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "publishers"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	pubBodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	var pubList []pubsub.PubSub
	err = json.Unmarshal(pubBodyBytes, &pubList)
	assert.Nil(t, err)
	defer resp.Body.Close()
	assert.Greater(t, len(pubList), 0)
}

func TestServer_TestPingStatusStatusCode(t *testing.T) {
	req, err := http.NewRequest("PUT", fmt.Sprintf("http://localhost:%d%s%s%s", port, apPath, "subscriptions/status/", ObjSub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.4 Delete individual subscription resources
// 5.3.4.5 (1) Expected Results: The return code is “204 DELETE”.
func TestServer_DeleteSubscription_OK(t *testing.T) {
	clients := server.GetSubscriberAPI().GetClientIDAddressByResource(ObjSub.Resource)
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "subscriptions", ObjSub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	defer resp.Body.Close()
	// clean up files on disk
	for clientID := range clients {
		os.Remove(fmt.Sprintf("%s.json", clientID))
	}
}

// O-RAN.WG6.O-CLOUD-CONF-Test-R003-v02.00
// TC5.3.4 Delete individual subscription resources
func TestServer_DeleteSubscription_KO_SubNotFound(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "subscriptions", ObjSub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	defer resp.Body.Close()
}

func TestServer_DeleteAllSubscriptions(t *testing.T) {
	// Delete All Subscriptions
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
}

func TestServer_DeletePublisher(t *testing.T) {
	// Delete All Publisher
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "publishers"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
}

func TestServer_GetNonExistingPublisher(t *testing.T) {
	// Get Just created Publisher
	req, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "publishers", ObjPub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Nil(t, err)
}

func TestServer_GetNonExistingSubscription(t *testing.T) {
	// Get Just Created Subscription
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "subscriptions", ObjSub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_TestDummyStatusCode(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "dummy"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestServer_KillAndRecover(t *testing.T) {
	server.Shutdown()
	time.Sleep(2 * time.Second)
	// CHECK URL IS UP
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "health"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// New get new rest client
func NewRestClient() *Rest {
	return &Rest{
		client: http.Client{
			Timeout: 1 * time.Second,
		},
	}
}

func publishEvent(e event.Event) {
	//create publisher
	url := &types.URI{URL: url.URL{Scheme: "http",
		Host: fmt.Sprintf("localhost:%d", port),
		Path: fmt.Sprintf("%s%s", apPath, "create/event")}}
	rc := NewRestClient()
	err := rc.PostEvent(url, e)
	if err != nil {
		log.Errorf("error publishing events %v to url %s", err, url.String())
	} else {
		log.Debugf("published event %s", e.ID)
	}
}

func Test_MultiplePost(t *testing.T) {
	pub := pubsub.PubSub{
		ID:          "",
		EndPointURI: &types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy")}},
		Resource:    resource,
	}
	pubData, err := json.Marshal(&pub)
	assert.Nil(t, err)
	assert.NotNil(t, pubData)
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "publishers"), bytes.NewBuffer(pubData))
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()
	pubBodyBytes, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	err = json.Unmarshal(pubBodyBytes, &ObjPub)
	assert.Nil(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.NotEmpty(t, ObjPub.ID)
	assert.NotEmpty(t, ObjPub.URILocation)
	assert.NotEmpty(t, ObjPub.EndPointURI)
	assert.NotEmpty(t, ObjPub.Resource)
	assert.Equal(t, pub.Resource, ObjPub.Resource)
	log.Infof("publisher \n%s", ObjPub.String())

	cneEvent := v1event.CloudNativeEvent()
	cneEvent.SetID(ObjPub.ID)
	cneEvent.Type = string(ptp.PtpStateChange)
	cneEvent.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
	cneEvent.SetDataContentType(event.ApplicationJSON)
	data := event.Data{
		Version: "event",
		Values: []event.DataValue{{
			Resource:  "test",
			DataType:  event.NOTIFICATION,
			ValueType: event.ENUMERATION,
			Value:     ptp.ACQUIRING_SYNC,
		},
		},
	}
	data.SetVersion("1.0") //nolint:errcheck
	cneEvent.SetData(data)
	for i := 0; i < 5; i++ {
		go publishEvent(cneEvent)
	}
	time.Sleep(2 * time.Second)
}

func TestServer_End(*testing.T) {
	for clientID := range server.GetSubscriberAPI().GetClientIDAddressByResource(ObjSub.Resource) {
		os.Remove(fmt.Sprintf("%s.json", clientID))
	}
	os.Remove("pub.json")
	os.Remove("sub.json")
	// hanlding go test -race ./...
	// by closing channel only once
	onceCloseEvent.Do(func() {
		close(eventOutCh)
	})

	onceCloseCloseCh.Do(func() {
		close(closeCh)
	})
}

// Rest client to make http request
type Rest struct {
	client http.Client
}

// Post post with data
func (r *Rest) Post(url *types.URI, data []byte) int {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", url.String(), bytes.NewBuffer(data))
	if err != nil {
		log.Errorf("error creating post request %v", err)
		return http.StatusBadRequest
	}
	request.Header.Set("content-type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		log.Errorf("error in post response %v", err)
		return http.StatusBadRequest
	}
	if response.Body != nil {
		defer response.Body.Close()
		// read any content and print
		body, readErr := io.ReadAll(response.Body)
		if readErr == nil && len(body) > 0 {
			log.Debugf("%s return response %s\n", url.String(), string(body))
		}
	}
	return response.StatusCode
}

// New get new rest client
func New() *Rest {
	return &Rest{
		client: http.Client{
			Timeout: 1 * time.Second,
		},
	}
}

// PostEvent post an event to the give url and check for error
func (r *Rest) PostEvent(url *types.URI, e event.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		log.Errorf("error marshalling event %v", e)
		return err
	}
	if status := r.Post(url, b); status == http.StatusBadRequest {
		return fmt.Errorf("post returned status %d", status)
	}
	return nil
}

func TestServerStatusConcurrency(*testing.T) {
	s := &restapi.Server{} // Adjust import as needed if your package name differs
	statuses := []restapi.ServerStatus{
		0,
		1,
		2,
		3,
	}
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine: updates status multiple times
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.SetStatus(statuses[i%len(statuses)])
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Reader goroutine: reads Ready() status
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = s.Ready()
			time.Sleep(1 * time.Millisecond)
		}
	}()

	wg.Wait()
}
