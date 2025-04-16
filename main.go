package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"
)

var callIDToOffer = make(map[string]*webrtc.PeerConnection)
var mutex = &sync.Mutex{}

type actionData struct {
	Action string
	Data   SessionDescription
}

var actionChannels = sync.Map{}

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

func createPeerConnection() (*webrtc.PeerConnection, error) {
	// config := webrtc.Configuration{
	// 	ICEServers: []webrtc.ICEServer{
	// 		{
	// 			URLs: []string{"stun:stun.l.google.com:19302"},
	// 		},
	// 	},
	// }
	config := webrtc.Configuration{}
	return webrtc.NewPeerConnection(config)
}

func generateSDPOffer(request OfferRequest) (Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	ch := make(chan actionData, 1)

	// Store peer connection
	callID := request.CallID
	// log.Println("Original Call ID:", callID)
	if callID == "" {
		callID = uuid.New().String()
	}
	// log.Println("Generated Call ID:", callID)

	actionChannels.Store(callID, ch)

	pc, err := createPeerConnection()
	if err != nil {
		return Event{}, err
	}

	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("%s ICE Connection State has changed: %s\n", callID, connectionState.String())
	})

	// ✅ Create an Opus track
	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion",
	)
	if err != nil {
		log.Println("❌ Error creating audio track:", err)
		pc.Close()
		return Event{}, err
	}

	// ✅ Add track to PeerConnection
	rtpSender, err := pc.AddTrack(audioTrack)
	if err != nil {
		log.Println("❌ Error adding audio track:", err)
		pc.Close()
		return Event{}, err
	}
	log.Println("✅ Audio track added successfully")

	// Create an offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return Event{}, err
	}

	// Start ICE gathering and wait for completion
	gatherComplete := webrtc.GatheringCompletePromise(pc)

	// Set local description FIRST to trigger ICE gathering
	err = pc.SetLocalDescription(offer)
	if err != nil {
		pc.Close()
		return Event{}, err
	}

	// ✅ Wait for ICE gathering to complete
	<-gatherComplete

	finalOffer := pc.LocalDescription()
	if finalOffer == nil {
		pc.Close()
		return Event{}, fmt.Errorf("failed to retrieve local description")
	}

	mutex.Lock()
	callIDToOffer[callID] = pc
	mutex.Unlock()

	// ✅ Auto remove PC after timeout
	go autoRemovePeerConnection(callID, 45*time.Second)

	offerResponse := OfferResponse{
		Offer: Offer{
			SDP:  finalOffer.SDP,
			Type: finalOffer.Type.String(),
		},
	}

	payload := createCallbackPayload(request, offerResponse.Offer, callID)

	if request.CallbackURL != "" {
		// Fire and forget (non-blocking)
		sendCallbackAsync(request.CallbackURL, payload)
	}

	go func() {
		defer actionChannels.Delete(callID)
		select {
		case action := <-ch:
			log.Printf("📩 Received action: %s %s\n", callID, action.Action)
			// Process the answer received from `processAction`
			if action.Action == "accept" {
				var sdpString string
				sdpString = action.Data.SDP

				remoteDesc := webrtc.SessionDescription{
					Type: webrtc.SDPTypeAnswer,
					SDP:  sdpString,
				}
				if err := pc.SetRemoteDescription(remoteDesc); err != nil {
					log.Printf("❌ Error setting remote description: %v", err)
					return
				}

				// Start streaming audio
				streamAudio(ctx, pc, "output.ogg", audioTrack, rtpSender, callID)
			}
		case <-ctx.Done():
			log.Printf("%s Timeout waiting for answer\n", callID)
		}
	}()

	log.Println("Request Processed ", callID)

	return payload, nil
}

// ✅ Auto remove PC after timeout
func autoRemovePeerConnection(callID string, duration time.Duration) {
	time.Sleep(duration)

	mutex.Lock()
	pc, exists := callIDToOffer[callID]
	if exists {
		pc.Close()
		delete(callIDToOffer, callID)
		log.Println("Auto-cleanup: Removed inactive call_id", callID)
	}
	mutex.Unlock()
}

func createCallbackPayload(request OfferRequest, offer Offer, callID string) Event {

	sdpData, err := json.Marshal(map[string]string{
		"type": offer.Type,
		"sdp":  offer.SDP,
	})
	if err != nil {
		fmt.Println("Error marshaling SDP:", err)
	}

	connection := map[string]any{
		"webrtc": map[string]string{
			"sdp": string(sdpData),
		},
	}

	// connection := map[string]any{
	// 	"webrtc": map[string]string{
	// 		"sdp":  offer.SDP,
	// 		"type": offer.Type,
	// 	},
	// }

	// Adding session field inside the connection
	session := map[string]any{
		"sdp":      offer.SDP,
		"sdp_type": offer.Type,
	}

	call := Call{
		ID:         callID,
		From:       request.From,
		To:         request.To, // Should be dynamic
		Event:      "connect",
		Timestamp:  fmt.Sprintf("%d", time.Now().Unix()),
		Direction:  "USER_INITIATED",
		Connection: connection,
		Session:    session,
		// Callback:   request.CallbackURL, // If empty, it's omitted due to `omitempty`
	}

	metadata := Metadata{
		DisplayPhoneNumber: "919999999999", // Replace dynamically if needed
		PhoneNumberID:      "00000000000000",
	}

	contacts := []map[string]any{
		{
			"profile": map[string]string{
				"name": "Gupshup Load",
			},
			"wa_id": "00000000000000",
		},
	}

	value := Value{
		MessagingProduct: "random",
		Metadata:         metadata,
		Contacts:         contacts,
		Calls:            []Call{call},
	}

	change := Change{
		Value: value,
		Field: "calls",
	}

	entry := Entry{
		ID:      "00000000000000", // Dynamic if needed
		Changes: []Change{change},
	}

	event := Event{
		Object: "random_business_account",
		Entry:  []Entry{entry},
	}

	return event
}

func sendCallbackAsync(callbackURL string, payload Event) {
	go func() { // Fire and forget
		client := &http.Client{Timeout: 10 * time.Second}
		jsonData, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", callbackURL, bytes.NewBuffer(jsonData))
		if err != nil {
			log.Printf("Error creating callback request: %v\n", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Error sending callback request: %v\n", err)
			return
		}
		defer resp.Body.Close()

		// body, _ := io.ReadAll(resp.Body)
		// log.Printf("Callback response: %s\n", string(body))
		log.Printf("Callback response status: %d\n", resp.StatusCode)
	}()
}

func streamAudio(ctx context.Context, pc *webrtc.PeerConnection, filename string, audioTrack *webrtc.TrackLocalStaticSample, rtpSender *webrtc.RTPSender, callID string) {
	log.Println("🎵 Starting audio streaming...")

	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("%s ICE Connection State has changed: %s\n", callID, connectionState.String())
	})

	// Wait for ICE connection to be established
	iceConnected := make(chan struct{})
	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		if connectionState == webrtc.ICEConnectionStateConnected {
			close(iceConnected)
		}
	})

	select {
	case <-iceConnected:
		log.Printf("%s ICE connection established\n", callID)
	case <-ctx.Done():
		log.Printf("%s ICE connection timeout\n", callID)
		return
	}

	//✅ Handle RTCP (optional debugging)
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			_, _, rtcpErr := rtpSender.Read(rtcpBuf)
			if rtcpErr != nil {
				return
			}
		}
	}()

	go func() {
		// ✅ Open Ogg file
		file, err := os.Open(filename)
		if err != nil {
			log.Println("❌ Error opening Ogg file:", err)
			return
		}
		defer file.Close()

		// ✅ Create an Ogg reader
		ogg, _, oggErr := oggreader.NewWith(file)
		if oggErr != nil {
			log.Println("❌ Error initializing Ogg reader:", oggErr)
			return
		}

		// ✅ Initialize timing
		var lastGranule uint64
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("%s Audio streaming stopped due to timeout\n", callID)
				return
			case <-ticker.C:
				// ✅ Read Ogg packet
				pageData, pageHeader, oggErr := ogg.ParseNextPage()
				if errors.Is(oggErr, io.EOF) {
					log.Printf("%s All audio pages parsed and sent\n", callID)
					return
				}
				if oggErr != nil {
					log.Printf("%s Error reading Ogg page: %v\n", callID, oggErr)
					return
				}

				sampleCount := float64(pageHeader.GranulePosition - lastGranule)
				lastGranule = pageHeader.GranulePosition
				sampleDuration := time.Duration((sampleCount/48000)*1000) * time.Millisecond

				if oggErr = audioTrack.WriteSample(media.Sample{Data: pageData, Duration: sampleDuration}); oggErr != nil {
					log.Printf("%s Error writing audio sample: %v\n", callID, oggErr)
					return
				}

				log.Printf("%s Sent Ogg packet of size %d bytes, duration %s\n", callID, len(pageData), sampleDuration)
			}
		}
	}()
}

func processAction(c *fiber.Ctx) error {
	var action ActionRequest
	if err := c.BodyParser(&action); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}
	log.Printf("📩 Parsed action request: %s %s\n", action.CallID, action.Action)

	mutex.Lock()
	pc, exists := callIDToOffer[action.CallID]
	mutex.Unlock()

	if !exists {
		// Return a proper JSON response with status, CallID, and Action details
		return c.JSON(fiber.Map{
			"status":  "No corresponding offer for this call_id or already closed",
			"call_id": action.CallID,
			"action":  action.Action,
		})
	}

	validCloseActions := map[string]bool{
		"terminate": true,
		"reject":    true,
		"hangup":    true,
	}

	if _, exists := validCloseActions[action.Action]; exists {
		pc.Close()
		mutex.Lock()
		delete(callIDToOffer, action.CallID)
		mutex.Unlock()
	}

	if action.Action == "accept" {
		var found bool
		var sdpString string
		if webrtcData, ok := action.Connection["webrtc"].(map[string]any); ok {
			if sdp, ok := webrtcData["sdp"].(string); ok {
				sdpString = sdp
				found = true
			}
		}

		if !found {
			if sessionData, ok := action.Session["sdp"].(string); ok {
				sdpString = sessionData
				found = true
			}
		}

		if !found {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "SDP data missing"})
		}
		// log.Printf("📩 SDP data found: %s\n", sdpString)
		// Send the action to the channel
		// log.Printf("📩 Sending action to channel for CallID: %s\n", action.CallID)

		// actionChannels.Range(func(key, value any) bool {
		// 	log.Printf("Key: %v, Value: %v\n", key, value)
		// 	return true // Continue iteration
		// })

		if ch, ok := actionChannels.Load(action.CallID); ok {
			log.Printf("📩 Sending action to channel: %s %s\n", action.CallID, action.Action)
			ch.(chan actionData) <- actionData{
				Action: action.Action,
				Data: SessionDescription{
					Type: "answer",
					SDP:  sdpString,
				},
			}
		}
	}

	return c.JSON(fiber.Map{"status": "Action processed successfully"})
}

func generateSDPAnswer(request AnswerRequest) (AnswerResponse, error) {
	pc, err := createPeerConnection()
	if err != nil {
		return AnswerResponse{}, err
	}

	// Handle Incoming Offer
	remoteDesc := webrtc.SessionDescription{
		SDP:  request.Session.SDP, // Fixed issue (Using correct struct)
		Type: webrtc.SDPTypeOffer,
	}
	if err := pc.SetRemoteDescription(remoteDesc); err != nil {
		pc.Close()
		return AnswerResponse{}, err
	}

	// ✅ Create an Opus track
	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion",
	)
	if err != nil {
		log.Println("❌ Error creating audio track:", err)
		pc.Close()
		return AnswerResponse{}, err
	}

	// ✅ Add track to PeerConnection
	// rtpSender, err := pc.AddTrack(audioTrack)
	rtpSender, err := pc.AddTrack(audioTrack)
	if err != nil {
		log.Println("❌ Error adding audio track:", err)
		pc.Close()
		return AnswerResponse{}, err
	}
	log.Println("✅ Audio track added successfully")

	// Create an Answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return AnswerResponse{}, err
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return AnswerResponse{}, err
	}
	<-gatherComplete

	callID := request.CallID
	if callID == "" {
		callID = uuid.New().String()
	}

	mutex.Lock()
	callIDToOffer[callID] = pc
	mutex.Unlock()

	go autoRemovePeerConnection(callID, 45*time.Second)
	go streamAudio(context.Background(), pc, "output.ogg", audioTrack, rtpSender, callID)

	return AnswerResponse{
		CallID: callID,
		Answer: SessionDescription{
			SDP:  pc.LocalDescription().SDP,
			Type: pc.LocalDescription().Type.String(),
		},
	}, nil
}

func processAnswer(c *fiber.Ctx) error {
	var request AnswerRequest
	if err := c.BodyParser(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request"})
	}

	if request.Action != "connect" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid action"})
	}

	response, err := generateSDPAnswer(request)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": fmt.Sprintf("Error generating answer: %v", err)})
	}

	return c.JSON(response)
}

func main() {

	port := flag.String("p", "8080", "Port to run the server on")
	flag.Parse()

	app := fiber.New()

	app.Use(logger.New(logger.Config{
		Format: "${time} | ${status} | ${method} | ${path} | ${latency}\n",
	}))

	app.Post("/load/offer", func(c *fiber.Ctx) error {
		var request OfferRequest
		if err := c.BodyParser(&request); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request"})
		}

		response, err := generateSDPOffer(request)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": fmt.Sprintf("Error generating offer: %v", err)})
		}

		// Return the response (it can be OfferResponse or a JSON payload)
		return c.JSON(response)
	})

	app.Post("/load/calls", processAnswer)

	app.Post("/load/action", processAction)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	go func() {
		<-quit
		log.Println("Shutting down server...")
		mutex.Lock()
		for _, pc := range callIDToOffer {
			pc.Close()
		}
		mutex.Unlock()
		os.Exit(0)
	}()

	log.Printf("🚀 Server running on port %s", *port)
	log.Fatal(app.Listen(":" + *port))
}
