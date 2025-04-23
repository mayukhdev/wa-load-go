package main

import (
	"sync"

	"github.com/pion/webrtc/v4"
)

type ActionData struct {
	Action string
	Data   SessionDescription
}

var ActionChannels = sync.Map{}

type CallIDDetails struct {
	pc *webrtc.PeerConnection
	ch chan ActionData
}

type Offer struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

type OfferRequest struct {
	To          string `json:"to"`
	CallbackURL string `json:"callback_url,omitempty"`
	CallID      string `json:"call_id,omitempty"`
	From        string `json:"from"`
}

type OfferResponse struct {
	CallID           string `json:"call_id"`
	Offer            Offer  `json:"offer"`
	CallbackResponse string `json:"callback_response,omitempty"`
}

type ActionRequest struct {
	CallID           string         `json:"call_id"`
	Action           string         `json:"action"`
	Connection       map[string]any `json:"connection,omitempty"`
	Session          map[string]any `json:"session,omitempty"`
	MessagingProduct string         `json:"messaging_product"`
}

type Call struct {
	ID         string         `json:"id"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Event      string         `json:"event"`
	Timestamp  string         `json:"timestamp"`
	Direction  string         `json:"direction"`
	Status     string         `json:"status,omitempty"`
	Connection map[string]any `json:"connection,omitempty"`
	Session    map[string]any `json:"session,omitempty"`
}

type Metadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

type Value struct {
	MessagingProduct string   `json:"messaging_product"`
	Calls            []Call   `json:"calls"`
	Metadata         Metadata `json:"metadata"`
	Contacts         any      `json:"contacts"`
}

type Change struct {
	Value Value  `json:"value"`
	Field string `json:"field"`
}

type Entry struct {
	ID      string   `json:"id"`
	Changes []Change `json:"changes"`
}

type Event struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

type SessionDescription struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

type AnswerResponse struct {
	CallID string             `json:"call_id"`
	Answer SessionDescription `json:"answer"`
}

type AnswerRequest struct {
	CallID           string             `json:"call_id"`
	To               string             `json:"to"`
	Action           string             `json:"action"`
	Session          SessionDescription `json:"session"`
	MessagingProduct string             `json:"messaging_product"`
	CallbackURL      string             `json:"callback_url,omitempty"`
	CallbackData     string             `json:"biz_opaque_callback_data,omitempty"`
}
