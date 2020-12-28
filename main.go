package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pion/webrtc/v3/pkg/media"

	"github.com/notedit/gst"

	"github.com/pion/webrtc/v3"
)

func main() {
	pcAnswer, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		log.Fatalf("failed to new pc: %+v", err)
	}
	connected := make(chan struct{})
	pcAnswer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[answer] ice state: %s", state)
		if state == webrtc.ICEConnectionStateConnected {
			close(connected)
		}
	})

	go func() {
		if err := startServeAudio(pcAnswer, connected); err != nil {
			log.Printf("failed to serve audio: %+v", err)
		}
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open("static/index.html")
		if err != nil {
			respondError(w, err)
			return
		}
		defer f.Close()
		b, err := ioutil.ReadAll(f)
		if err != nil {
			respondError(w, err)
			return
		}
		w.Header().Set("content-type", "text/html")
		if _, err := w.Write(b); err != nil {
			respondError(w, err)
			return
		}
	})

	http.HandleFunc("/signal", func(w http.ResponseWriter, r *http.Request) {
		rb, err := ioutil.ReadAll(r.Body)
		if err != nil {
			respondError(w, fmt.Errorf("failed to read offer request body: %+v", err))
			return
		}
		offer, err := decodeOffer(string(rb))
		if err != nil {
			respondError(w, fmt.Errorf("failed to decode offer body: %+v", err))
			return
		}
		answer, err := createAnswer(pcAnswer, offer)
		if err != nil {
			respondError(w, fmt.Errorf("failed to create answer: %+v", err))
			return
		}
		eAnswer, err := encodeAnswer(answer)
		if err != nil {
			respondError(w, fmt.Errorf("failed to encode answer: %+v", err))
			return
		}
		if _, err := w.Write([]byte(eAnswer)); err != nil {
			respondError(w, fmt.Errorf("failed to write response: %+v", err))
			return
		}
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func respondError(w http.ResponseWriter, err error) {
	log.Printf("error: %+v", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func decodeOffer(in string) (*webrtc.SessionDescription, error) {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		return nil, err
	}
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(b, &offer); err != nil {
		return nil, err
	}
	return &offer, nil
}

func encodeAnswer(answer *webrtc.SessionDescription) (string, error) {
	b, err := json.Marshal(answer)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func startServeAudio(pc *webrtc.PeerConnection, connected chan struct{}) error {
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion")
	if err != nil {
		return fmt.Errorf("failed to create track: %+v", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		return fmt.Errorf("failed to add track: %+v", err)
	}

	go func() {
		<-connected
		log.Printf("start audio streaming")

		stream, err := GetOpusAudioStream()
		if err != nil {
			log.Printf("failed to get stream: %+v", err)
			return
		}
		for {
			gstSample, err := stream.PullSample()
			if err != nil {
				log.Printf("failed to read chunk: %+v", err)
				break
			}
			if err := track.WriteSample(media.Sample{Data: gstSample.Data, Duration: time.Duration(gstSample.Duration)}); err != nil {
				log.Printf("failed to write sample: %+v", err)
				break
			}
		}
	}()
	return nil
}

type gstStream struct {
	gstPipeline *gst.Pipeline
	gstElement  *gst.Element
}

func (s *gstStream) Close() error {
	s.gstPipeline.SetState(gst.StateNull)
	return nil
}

func (s *gstStream) PullSample() (*gst.Sample, error) {
	sample, err := s.gstElement.PullSample()
	if err != nil {
		if s.gstElement.IsEOS() {
			return nil, io.EOF
		}
		return nil, err
	}
	return sample, nil
}

func GetOpusAudioStream() (*gstStream, error) {
	if err := gst.CheckPlugins([]string{"pulseaudio", "opus"}); err != nil {
		return nil, err
	}
	pipelineStr := fmt.Sprintf("pulsesrc server=localhost:4713 ! opusenc ! appsink name=audio")
	log.Printf("starting gstreamer pipeline: %s", pipelineStr)
	pipeline, err := gst.ParseLaunch(pipelineStr)
	if err != nil {
		return nil, err
	}

	element := pipeline.GetByName("audio")
	pipeline.SetState(gst.StatePlaying)
	return &gstStream{
		gstPipeline: pipeline,
		gstElement:  element,
	}, nil
}

func createAnswer(pcAnswer *webrtc.PeerConnection, offer *webrtc.SessionDescription) (*webrtc.SessionDescription, error) {
	if err := pcAnswer.SetRemoteDescription(*offer); err != nil {
		return nil, err
	}
	answer, err := pcAnswer.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}
	answerGatheringComplete := webrtc.GatheringCompletePromise(pcAnswer)
	if err = pcAnswer.SetLocalDescription(answer); err != nil {
		return nil, err
	}
	<-answerGatheringComplete
	return pcAnswer.LocalDescription(), nil
}
