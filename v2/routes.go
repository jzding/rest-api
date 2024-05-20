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

package restapi

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/redhat-cne/sdk-go/pkg/types"

	"github.com/redhat-cne/rest-api/pkg/localmetrics"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/redhat-cne/sdk-go/pkg/subscriber"

	cne "github.com/redhat-cne/sdk-go/pkg/event"
	"github.com/redhat-cne/sdk-go/pkg/pubsub"

	"github.com/redhat-cne/sdk-go/v1/event"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/redhat-cne/sdk-go/pkg/channel"

	"log"
	"net/http"
)

// createSubscription create subscription and send it to a channel that is shared by middleware to process
// Creates a new subscription .
// If subscription exists with same resource then existing subscription is returned .
// responses:
//
//	201: repoResp
//	400: badReq
//	204: noContent
func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	log.Printf("DZK in V2 createSubscription")
	log.Printf("DZK createSubscription request r.Header=%v, r.Host=%s, r.URL=%v, r.URL.Host=%s, r.RemoteAddr=%s, r.RequestURI=%s", r.Header, r.Host, r.URL, r.URL.Host, r.RemoteAddr, r.RequestURI)
	defer r.Body.Close()
	var response *http.Response
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sub := pubsub.PubSub{}
	if err = json.Unmarshal(bodyBytes, &sub); err != nil {
		respondWithError(w, "marshalling error")
		localmetrics.UpdateSubscriptionCount(localmetrics.FAILCREATE, 1)
		return
	}
	log.Printf("DZK subscription 1 sub=%v", sub)
	endPointURI := sub.GetEndpointURI()
	log.Printf("DZK subscription 1.1 endPointURI=%s", endPointURI)
	if endPointURI != "" {
		response, err = s.HTTPClient.Post(sub.GetEndpointURI(), cloudevents.ApplicationJSON, nil)
		if err != nil {
			log.Printf("there was error validating endpointurl %v, subscription wont be created", err)
			localmetrics.UpdateSubscriptionCount(localmetrics.FAILCREATE, 1)
			respondWithError(w, err.Error())
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			log.Printf("there was an error validating endpointurl %s returned status code %d", sub.GetEndpointURI(), response.StatusCode)
			respondWithError(w, "return url validation check failed for create subscription.check endpointURI")
			localmetrics.UpdateSubscriptionCount(localmetrics.FAILCREATE, 1)
			return
		}
	}
	// check sub.EndpointURI by get
	id := uuid.New().String()
	sub.SetID(id)
	_ = sub.SetURILocation(fmt.Sprintf("http://localhost:%d%s%s/%s", s.port, s.apiPath, "subscriptions", sub.ID)) //nolint:errcheck

	log.Printf("DZK subscription 2 sub=%v", sub)

	newSub, err := s.pubSubAPI.CreateSubscription(sub)
	if err != nil {
		log.Printf("error creating subscription %v", err)
		localmetrics.UpdateSubscriptionCount(localmetrics.FAILCREATE, 1)
		respondWithError(w, err.Error())
		return
	}
	log.Printf("subscription created successfully.")

	addr := newSub.GetResource()
	// create unique clientId for each subscription based on endPointURI
	subs := subscriber.New(s.getClientIDFromURI(endPointURI))

	_ = subs.SetEndPointURI(endPointURI)

	// create a subscriber model
	//	subs.AddSubscription(newSub)
	subs.AddSubscription(newSub)
	subs.Action = channel.NEW
	log.Printf("DZK subscription 3 subs=%v", subs)

	cevent, _ := subs.CreateCloudEvents()
	cevent.SetSubject(channel.NEW.String())
	cevent.SetSource(addr)

	out := channel.DataChan{
		Address: addr,
		Data:    cevent,
		Status:  channel.NEW,
		Type:    channel.SUBSCRIBER,
	}

	var updatedObj *subscriber.Subscriber
	// writes a file <clientID>.json that has the same content as configMap.
	// configMap was created later as a way to persist the data.
	if updatedObj, err = s.subscriberAPI.CreateSubscription(subs.ClientID, *subs); err != nil {
		log.Printf("failed creating subscription for %s", subs.ClientID.String())
		out.Status = channel.FAILED
	} else {
		out.Status = channel.SUCCESS
		_ = out.Data.SetData(cloudevents.ApplicationJSON, updatedObj)
		log.Printf("DZK subscription 4 out.Data=%v", out.Data)
		// TODO: this function is in sdk-go
		// localmetrics.UpdateSenderCreatedCount(obj.ClientID.String(), localmetrics.ACTIVE, 1)

		log.Printf("subscription created successfully.")
		localmetrics.UpdateSubscriptionCount(localmetrics.ACTIVE, 1)
		respondWithJSON(w, http.StatusCreated, newSub)
	}

	s.dataOut <- &out
}

// createPublisher create publisher and send it to a channel that is shared by middleware to process
// Creates a new publisher .
// If publisher exists with same resource then existing publisher is returned .
// responses:
//
//	201: repoResp
//	400: badReq
//	204: noContent
func (s *Server) createPublisher(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var response *http.Response
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	pub := pubsub.PubSub{}
	if err = json.Unmarshal(bodyBytes, &pub); err != nil {
		localmetrics.UpdatePublisherCount(localmetrics.FAILCREATE, 1)
		respondWithError(w, "marshalling error")
		return
	}
	if pub.GetEndpointURI() != "" {
		response, err = s.HTTPClient.Post(pub.GetEndpointURI(), cloudevents.ApplicationJSON, nil)
		if err != nil {
			log.Printf("there was an error validating the publisher endpointurl %v, publisher won't be created.", err)
			localmetrics.UpdatePublisherCount(localmetrics.FAILCREATE, 1)
			respondWithError(w, err.Error())
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			log.Printf("there was an error validating endpointurl %s returned status code %d", pub.GetEndpointURI(), response.StatusCode)
			localmetrics.UpdatePublisherCount(localmetrics.FAILCREATE, 1)
			respondWithError(w, "return url validation check failed for create publisher,check endpointURI")
			return
		}
	}

	// check pub.EndpointURI by get
	pub.SetID(uuid.New().String())
	_ = pub.SetURILocation(fmt.Sprintf("http://localhost:%d%s%s/%s", s.port, s.apiPath, "publishers", pub.ID)) //nolint:errcheck
	newPub, err := s.pubSubAPI.CreatePublisher(pub)
	if err != nil {
		log.Printf("error creating publisher %v", err)
		localmetrics.UpdatePublisherCount(localmetrics.FAILCREATE, 1)
		respondWithError(w, err.Error())
		return
	}
	log.Printf("publisher created successfully.")
	// go ahead and create QDR to this address
	s.sendOut(channel.PUBLISHER, &newPub)
	localmetrics.UpdatePublisherCount(localmetrics.ACTIVE, 1)
	respondWithJSON(w, http.StatusCreated, newPub)
}

func (s *Server) sendOut(eType channel.Type, sub *pubsub.PubSub) {
	// go ahead and create QDR to this address
	s.dataOut <- &channel.DataChan{
		ID:      sub.GetID(),
		Address: sub.GetResource(),
		Data:    &ce.Event{},
		Type:    eType,
		Status:  channel.NEW,
	}
}

func (s *Server) sendOutToDelete(eType channel.Type, sub *pubsub.PubSub) {
	// go ahead and create QDR to this address
	s.dataOut <- &channel.DataChan{
		ID:      sub.GetID(),
		Address: sub.GetResource(),
		Data:    &ce.Event{},
		Type:    eType,
		Status:  channel.DELETE,
	}
}

func (s *Server) getSubscriptionByID(w http.ResponseWriter, r *http.Request) {
	queries := mux.Vars(r)
	subscriptionID, ok := queries["subscriptionid"]
	if !ok {
		respondWithError(w, "subscription not found")
		return
	}
	sub, err := s.pubSubAPI.GetSubscription(subscriptionID)
	if err != nil {
		respondWithError(w, "subscription not found")
		return
	}
	respondWithJSON(w, http.StatusOK, sub)
}

func (s *Server) getPublisherByID(w http.ResponseWriter, r *http.Request) {
	queries := mux.Vars(r)
	publisherID, ok := queries["publisherid"]
	if !ok {
		respondWithError(w, "publisher parameter is required")
		return
	}
	pub, err := s.pubSubAPI.GetPublisher(publisherID)
	if err != nil {
		respondWithError(w, "publisher not found")
		return
	}
	respondWithJSON(w, http.StatusOK, pub)
}
func (s *Server) getSubscriptions(w http.ResponseWriter, _ *http.Request) {
	b, err := s.pubSubAPI.GetSubscriptionsFromFile()
	if err != nil {
		respondWithError(w, "error loading subscriber data")
		return
	}
	respondWithByte(w, http.StatusOK, b)
}

func (s *Server) getPublishers(w http.ResponseWriter, _ *http.Request) {
	b, err := s.pubSubAPI.GetPublishersFromFile()
	if err != nil {
		respondWithError(w, "error loading publishers data")
		return
	}
	respondWithByte(w, http.StatusOK, b)
}

func (s *Server) deletePublisher(w http.ResponseWriter, r *http.Request) {
	queries := mux.Vars(r)
	publisherID, ok := queries["publisherid"]
	if !ok {
		respondWithError(w, "publisherid param is missing")
		return
	}

	if err := s.pubSubAPI.DeletePublisher(publisherID); err != nil {
		localmetrics.UpdatePublisherCount(localmetrics.FAILDELETE, 1)
		respondWithError(w, err.Error())
		return
	}

	localmetrics.UpdatePublisherCount(localmetrics.ACTIVE, -1)
	respondWithMessage(w, http.StatusOK, "OK")
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	queries := mux.Vars(r)
	subscriptionID, ok := queries["subscriptionid"]
	if !ok {
		respondWithError(w, "subscriptionid param is missing")
		return
	}

	if err := s.pubSubAPI.DeleteSubscription(subscriptionID); err != nil {
		localmetrics.UpdateSubscriptionCount(localmetrics.FAILDELETE, 1)
		respondWithError(w, err.Error())
		return
	}

	localmetrics.UpdateSubscriptionCount(localmetrics.ACTIVE, -1)
	respondWithMessage(w, http.StatusOK, "OK")
}

func (s *Server) deleteAllSubscriptions(w http.ResponseWriter, _ *http.Request) {
	size := len(s.pubSubAPI.GetSubscriptions())
	if err := s.pubSubAPI.DeleteAllSubscriptions(); err != nil {
		respondWithError(w, err.Error())
		return
	}
	//update metrics
	if size > 0 {
		localmetrics.UpdateSubscriptionCount(localmetrics.ACTIVE, -(size))
	}
	// go ahead and create QDR to this address
	s.sendOutToDelete(channel.SUBSCRIBER, &pubsub.PubSub{ID: "", Resource: "delete-all-subscriptions"})
	respondWithMessage(w, http.StatusOK, "deleted all subscriptions")
}

func (s *Server) deleteAllPublishers(w http.ResponseWriter, _ *http.Request) {
	size := len(s.pubSubAPI.GetPublishers())

	if err := s.pubSubAPI.DeleteAllPublishers(); err != nil {
		respondWithError(w, err.Error())
		return
	}
	//update metrics
	if size > 0 {
		localmetrics.UpdatePublisherCount(localmetrics.ACTIVE, -(size))
	}
	respondWithMessage(w, http.StatusOK, "deleted all publishers")
}

// publishEvent gets cloud native events and converts it to cloud event and publishes to a transport to send
// it to the consumer
func (s *Server) publishEvent(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		respondWithError(w, err.Error())
		return
	}
	cneEvent := event.CloudNativeEvent()
	if err = json.Unmarshal(bodyBytes, &cneEvent); err != nil {
		respondWithError(w, err.Error())
		return
	} // check if publisher is found
	pub, err := s.pubSubAPI.GetPublisher(cneEvent.ID)
	if err != nil {
		localmetrics.UpdateEventPublishedCount(cneEvent.ID, localmetrics.FAIL, 1)
		respondWithError(w, fmt.Sprintf("no publisher data for id %s found to publish event for", cneEvent.ID))
		return
	}
	ceEvent, err := cneEvent.NewCloudEvent(&pub)
	if err != nil {
		localmetrics.UpdateEventPublishedCount(pub.Resource, localmetrics.FAIL, 1)
		respondWithError(w, err.Error())
	} else {
		s.dataOut <- &channel.DataChan{
			Type:    channel.EVENT,
			Data:    ceEvent,
			Address: pub.GetResource(),
		}
		localmetrics.UpdateEventPublishedCount(pub.Resource, localmetrics.SUCCESS, 1)
		respondWithMessage(w, http.StatusAccepted, "Event sent")
	}
}

// getCurrentState get current status of the  events that are subscribed to
func (s *Server) getCurrentState(w http.ResponseWriter, r *http.Request) {
	log.Printf("DZK getCurrentState request r.Header=%v, r.Host=%s, r.URL=%v, r.URL.Host=%s, r.RemoteAddr=%s, r.RequestURI=%s", r.Header, r.Host, r.URL, r.URL.Host, r.RemoteAddr, r.RequestURI)
	//s.getClientIDFromURI(endPointURI)
	queries := mux.Vars(r)
	resourceAddress, ok := queries["resourceAddress"]
	if !ok {
		respondWithError(w, "resourceAddress parameter not found")
		return
	}

	log.Printf("DZK num of subscriptions=%d, num of publishers=%d", len(s.pubSubAPI.GetSubscriptions()), len(s.pubSubAPI.GetPublishers()))

	//identify publisher or subscriber is asking for status
	var sub *pubsub.PubSub
	if len(s.pubSubAPI.GetSubscriptions()) > 0 {
		for _, subscriptions := range s.pubSubAPI.GetSubscriptions() {
			if strings.Contains(subscriptions.GetResource(), resourceAddress) {
				sub = subscriptions
				log.Printf("DZK found subscription %v", sub)
				break
			}
		}
	} else if len(s.pubSubAPI.GetPublishers()) > 0 {
		for _, publishers := range s.pubSubAPI.GetPublishers() {
			if strings.Contains(publishers.GetResource(), resourceAddress) {
				sub = publishers
				log.Printf("DZK found publisher %v", sub)
				break
			}
		}
	} else {
		respondWithError(w, "subscription not found")
		return
	}

	if sub == nil {
		respondWithError(w, "subscription not found")
		return
	}
	cneEvent := event.CloudNativeEvent()
	cneEvent.SetID(sub.ID)
	cneEvent.Type = channel.STATUS.String()
	cneEvent.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
	cneEvent.SetDataContentType(cloudevents.ApplicationJSON)
	cneEvent.SetData(cne.Data{
		Version: "v1",
	})
	ceEvent, err := cneEvent.NewCloudEvent(sub)

	if err != nil {
		respondWithError(w, err.Error())
		return
	}

	// params := mux.Vars(req)
	// clientID := params["clientID"]
	// resource := params["resourceAddress"]
	// clientUUID, parseError := uuid.Parse(clientID)

	// if parseError != nil || (resource == "" && clientID == "") {
	// 	_ = json.NewEncoder(w).Encode(map[string]string{"message": "validation failed, resource or clientID is empty"})
	// }

	// if !strings.HasPrefix(resource, "/") {
	// 	resource = fmt.Sprintf("/%s", resource)
	// }
	// // this is placeholder not sending back to report
	// out := channel.DataChan{
	// 	Address:  resource,
	// 	ClientID: clientUUID,
	// 	Status:   channel.NEW,
	// 	Type:     channel.STATUS, // could be new event of new subscriber (sender)
	// }
	// // validate client has the subscription for the resource
	// if _, sub := h.subscriberAPI.HasClient(clientUUID); !sub {
	// 	out.Status = channel.FAILED
	// 	localmetrics.UpdateStatusCheckCount(out.Address, localmetrics.FAILED, 1)
	// 	w.WriteHeader(http.StatusBadRequest)
	// 	_ = json.NewEncoder(w).Encode(map[string]string{"message": fmt.Sprintf("client is not registered with the event publisher %s ", h.ServiceName)})
	// } else if _, ok := h.subscriberAPI.HasSubscription(clientUUID, resource); !ok {
	// 	out.Status = channel.FAILED
	// 	localmetrics.UpdateStatusCheckCount(out.Address, localmetrics.FAILED, 1)
	// 	w.WriteHeader(http.StatusBadRequest)
	// 	_ = json.NewEncoder(w).Encode(map[string]string{"message": fmt.Sprintf("subscription for (%s) not found for the requesting client %s", resource, clientID)})
	// } else {
	// 	e, _ := out.CreateCloudEvents(CURRENTSTATE)
	// 	e.SetSource(resource)
	// 	// statusReceiveOverrideFn must return value for
	// 	if h.statusReceiveOverrideFn != nil {
	// 		if statusErr := h.statusReceiveOverrideFn(*e, &out); statusErr != nil {
	// 			out.Status = channel.FAILED
	// 			//out.Data here has the event to be published send it back
	// 			localmetrics.UpdateStatusCheckCount(out.Address, localmetrics.FAILED, 1)
	// 			w.WriteHeader(http.StatusBadRequest)
	// 			_ = json.NewEncoder(w).Encode(map[string]string{"message": statusErr.Error()})
	// 		} else if out.Data != nil {
	// 			localmetrics.UpdateStatusCheckCount(out.Address, localmetrics.SUCCESS, 1)
	// 			out.Status = channel.SUCCESS
	// 			w.Header().Set("Content-Type", "application/json")
	// 			w.WriteHeader(http.StatusOK)
	// 			_ = json.NewEncoder(w).Encode(*out.Data)
	// 		} else {
	// 			out.Status = channel.FAILED
	// 			w.WriteHeader(http.StatusBadRequest)
	// 			_ = json.NewEncoder(w).Encode(map[string]string{"message": "resource not found"})
	// 		}
	// 	} else {
	// 		out.Status = channel.FAILED
	// 		w.WriteHeader(http.StatusBadRequest)
	// 		_ = json.NewEncoder(w).Encode(map[string]string{"message": "onReceive function not defined"})
	// 	}
	// }

	// for http you send to the protocol address
	statusChannel := make(chan *channel.StatusChan, 1)
	s.dataOut <- &channel.DataChan{
		Type:       channel.STATUS,
		Data:       ceEvent,
		Address:    sub.GetResource(),
		StatusChan: statusChannel,
	}
	select {
	case d := <-statusChannel:
		if d.Data == nil || d.StatusCode != http.StatusOK {
			if string(d.Message) == "" {
				d.Message = []byte("event not found")
			}
			respondWithError(w, string(d.Message))
		} else {
			respondWithJSON(w, d.StatusCode, *d.Data)
		}
	case <-time.After(5 * time.Second):
		close(statusChannel)
		respondWithError(w, "timeout waiting for status")
	}
}

// pingForSubscribedEventStatus sends ping to the listening address in the producer to fire all status as events
func (s *Server) pingForSubscribedEventStatus(w http.ResponseWriter, r *http.Request) {
	queries := mux.Vars(r)
	subscriptionID, ok := queries["subscriptionid"]
	if !ok {
		respondWithError(w, "subscription parameter not found")
		return
	}
	sub, err := s.pubSubAPI.GetSubscription(subscriptionID)
	if err != nil {
		respondWithError(w, "subscription not found")
		return
	}
	cneEvent := event.CloudNativeEvent()
	cneEvent.SetID(sub.ID)
	cneEvent.Type = "status_check"
	cneEvent.SetTime(types.Timestamp{Time: time.Now().UTC()}.Time)
	cneEvent.SetDataContentType(cloudevents.ApplicationJSON)
	cneEvent.SetData(cne.Data{

		Version: "v1",
	})
	ceEvent, err := cneEvent.NewCloudEvent(&sub)

	if err != nil {
		respondWithError(w, err.Error())
	} else {
		s.dataOut <- &channel.DataChan{
			Type:       channel.STATUS,
			StatusChan: nil,
			Data:       ceEvent,
			Address:    fmt.Sprintf("%s/%s", sub.GetResource(), "status"),
		}
		respondWithMessage(w, http.StatusAccepted, "ping sent")
	}
}

// logEvent gets cloud native events and converts it to cloud native event and writes to log
func (s *Server) logEvent(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		respondWithError(w, err.Error())
		return
	}
	cneEvent := event.CloudNativeEvent()
	if err := json.Unmarshal(bodyBytes, &cneEvent); err != nil {
		respondWithError(w, err.Error())
		return
	} // check if publisher is found
	log.Printf("event received %v", cneEvent)
	respondWithMessage(w, http.StatusAccepted, "Event published to log")
}

func (s *Server) getClientIDFromURI(uri string) uuid.UUID {
	return uuid.NewMD5(uuid.NameSpaceURL, []byte(uri))
}

func dummy(w http.ResponseWriter, _ *http.Request) {
	respondWithMessage(w, http.StatusNoContent, "dummy test")
}

func respondWithError(w http.ResponseWriter, message string) {
	respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": message})
}

func respondWithJSON(w http.ResponseWriter, code int, payload interface{}) {
	if response, err := json.Marshal(payload); err == nil {
		w.Header().Set("Content-Type", cloudevents.ApplicationJSON)
		w.WriteHeader(code)
		w.Write(response) //nolint:errcheck
	} else {
		respondWithMessage(w, http.StatusBadRequest, "error parsing event data")
	}
}

func respondWithMessage(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", cloudevents.ApplicationJSON)
	respondWithJSON(w, code, map[string]string{"status": message})
}

func respondWithByte(w http.ResponseWriter, code int, message []byte) {
	w.Header().Set("Content-Type", cloudevents.ApplicationJSON)
	w.WriteHeader(code)
	w.Write(message) //nolint:errcheck
}
