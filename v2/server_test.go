// Copyright 2020 The Cloud Native Events Authors
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

	restapi "github.com/redhat-cne/rest-api"
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

	eventOutCh             chan *channel.DataChan
	closeCh                chan struct{}
	wg                     sync.WaitGroup
	port                   = 8989
	apPath                 = "/routes/cne/v1/"
	resource               = "test/test"
	resourceNoneSubscribed = "test/nonesubscribed"
	storePath              = "."
	ObjSub                 pubsub.PubSub
	ObjPub                 pubsub.PubSub
)

func init() {
	eventOutCh = make(chan *channel.DataChan, 10)
	closeCh = make(chan struct{})
}

func TestMain(m *testing.M) {
	server = restapi.InitServer(port, apPath, storePath, eventOutCh, closeCh)
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
					Version: "event",
					Values: []event.DataValue{{
						Resource:  "test",
						DataType:  event.NOTIFICATION,
						ValueType: event.ENUMERATION,
						Value:     ptp.ACQUIRING_SYNC,
					},
					},
				}
				data.SetVersion("v1") //nolint:errcheck
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
				_ = e.SetData(cloudevents.ApplicationJSON, cneEvent)
				func() {
					defer func() {
						if err := recover(); err != nil {
							log.Errorf("error on close channel")
						}
					}()
					if d.Address == resourceNoneSubscribed {
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

func TestServer_CreateSubscription(t *testing.T) {
	// create subscription
	sub := api.NewPubSub(
		&types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy")}},
		resource)

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

func TestServer_GetSubscription(t *testing.T) {
	// Get Just Created Subscription
	req, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, "subscriptions", ObjSub.ID), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
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
	var subList []pubsub.PubSub
	log.Printf("TestServer_ListSubscriptions :%s\n", string(bodyBytes))
	err = json.Unmarshal(bodyBytes, &subList)
	assert.Nil(t, err)
	assert.Greater(t, len(subList), 0)
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

func TestServer_GetCurrentState_withSubscription(t *testing.T) {
	// create subscription
	sub := api.NewPubSub(
		&types.URI{URL: url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", port), Path: fmt.Sprintf("%s%s", apPath, "dummy")}},
		resource)

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

	// try getting event
	time.Sleep(2 * time.Second)
	req, err = http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, ObjSub.Resource, "CurrentState"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err = server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	s, err2 := io.ReadAll(resp.Body)
	assert.Nil(t, err2)
	log.Infof("tedt %s ", string(s))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Delete All Subscriptions
	req, err = http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("http://localhost:%d%s%s", port, apPath, "subscriptions"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err = server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
}

func TestServer_GetCurrentState_withoutSubscription(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// try getting event
	time.Sleep(2 * time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%d%s%s/%s", port, apPath, resourceNoneSubscribed, "CurrentState"), nil)
	assert.Nil(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.HTTPClient.Do(req)
	assert.Nil(t, err)
	defer resp.Body.Close()
	s, err2 := io.ReadAll(resp.Body)
	assert.Nil(t, err2)
	log.Infof("tedt %s ", string(s))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
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

func TestServer_DeleteSubscription(t *testing.T) {
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
	assert.Equal(t, resp.StatusCode, http.StatusBadRequest)
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
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
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
	data.SetVersion("v1") //nolint:errcheck
	cneEvent.SetData(data)
	for i := 0; i < 5; i++ {
		go publishEvent(cneEvent)
	}
	time.Sleep(2 * time.Second)
}

func TestServer_End(*testing.T) {
	close(eventOutCh)
	close(closeCh)
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
