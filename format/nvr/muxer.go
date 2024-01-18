package nvr

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/aacparser"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/deepch/vdk/format/mp4"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var MIME = []byte{11, 22, 111, 222, 11, 22, 111, 222}
var listTag = []string{"{server_id}", "{hostname_short}", "{hostname_long}", "{stream_name}", "{channel_name}", "{stream_id}", "{channel_id}", "{start_year}", "{start_month}", "{start_day}", "{start_minute}", "{start_second}", "{start_millisecond}", "{start_unix_second}", "{start_unix_millisecond}", "{start_time}", "{start_pts}", "{end_year}", "{end_month}", "{end_day}", "{end_minute}", "{end_second}", "{end_millisecond}", "{start_unix_second}", "{start_unix_millisecond}", "{end_time}", "{end_pts}", "{duration_second}", "{duration_millisecond}"}

const (
	MP4 = "mp4"
	NVR = "nvr"
)

type Muxer struct {
	muxer                                                            *mp4.Muxer
	format                                                           string
	limit                                                            int
	d                                                                *os.File
	m                                                                *os.File
	dur                                                              time.Duration
	h                                                                int
	gof                                                              *Gof
	patch                                                            string
	start, end                                                       time.Time
	pstart, pend                                                     time.Duration
	started                                                          bool
	serverID, streamName, channelName, streamID, channelID, hostname string
}

type Gof struct {
	Streams []av.CodecData
	Packet  []av.Packet
}

type Data struct {
	Time  int64
	Start int64
	Dur   int64
}

const (
	B  = 1
	KB = 1024 * B
	MB = 1024 * KB
	GB = 1024 * MB
)

func init() {
	gob.RegisterName("nvr.Gof", Gof{})
	gob.RegisterName("h264parser.CodecData", h264parser.CodecData{})
	gob.RegisterName("aacparser.CodecData", aacparser.CodecData{})

}

func NewMuxer(serverID, streamName, channelName, streamID, channelID, patch string, format string, limit int) (m *Muxer, err error) {
	hostname, _ := os.Hostname()
	m = &Muxer{
		patch:       patch,
		h:           -1,
		gof:         &Gof{},
		format:      format,
		limit:       limit,
		serverID:    serverID,
		streamName:  streamName,
		channelName: channelName,
		streamID:    streamID,
		channelID:   channelID,
		hostname:    hostname,
	}
	return
}

func (m *Muxer) WriteHeader(streams []av.CodecData) (err error) {
	m.gof.Streams = streams
	if m.format == MP4 {
		m.OpenMP4()
	}

	return
}

func (m *Muxer) WritePacket(pkt av.Packet) (err error) {
	if len(m.gof.Streams) == 0 {
		return
	}
	if !m.started && pkt.IsKeyFrame {
		m.started = true
	}
	if m.started {
		switch m.format {
		case MP4:
			return m.writePacketMP4(pkt)
		case NVR:
			return m.writePacketNVR(pkt)
		}
	}

	return
}

func (m *Muxer) writePacketMP4(pkt av.Packet) (err error) {
	if pkt.IsKeyFrame && m.dur > time.Duration(m.limit)*time.Second {
		m.pstart = pkt.Time
		m.OpenMP4()
		m.dur = 0
	}
	m.dur += pkt.Duration
	m.pend = pkt.Time
	return m.muxer.WritePacket(pkt)
}

func (m *Muxer) writePacketNVR(pkt av.Packet) (err error) {
	if pkt.IsKeyFrame {
		if len(m.gof.Packet) > 0 {
			if err = m.writeGop(); err != nil {
				return
			}
		}
		m.gof.Packet, m.dur = nil, 0
	}
	if pkt.Idx == 0 {
		m.dur += pkt.Duration
	}
	m.gof.Packet = append(m.gof.Packet, pkt)

	return
}

func (m *Muxer) writeGop() (err error) {
	t := time.Now().UTC()
	if m.h != t.Hour() {
		if err = m.OpenNVR(); err != nil {
			return
		}
	}
	f := Data{
		Time: t.UnixNano(),
		Dur:  m.dur.Milliseconds(),
	}
	if f.Start, err = m.d.Seek(0, 2); err != nil {
		return
	}
	enc := gob.NewEncoder(m.d)
	if err = enc.Encode(m.gof); err != nil {
		return
	}
	buf := bytes.NewBuffer([]byte{})
	if err = binary.Write(buf, binary.LittleEndian, f); err != nil {
		return
	}
	if _, err = buf.Write(MIME); err != nil {
		return
	}
	_, err = m.m.Write(buf.Bytes())

	return
}

func (m *Muxer) OpenNVR() (err error) {
	m.WriteTrailer()
	t := time.Now().UTC()
	if err = os.MkdirAll(fmt.Sprintf("%s/%s", m.patch, t.Format("2006/01/02")), 0755); err != nil {
		return
	}
	if m.d, err = os.OpenFile(fmt.Sprintf("%s/%s/%d.d", m.patch, t.Format("2006/01/02"), t.Hour()), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0660); err != nil {
		return
	}
	if m.m, err = os.OpenFile(fmt.Sprintf("%s/%s/%d.m", m.patch, t.Format("2006/01/02"), t.Hour()), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0660); err != nil {
		return
	}
	m.h = t.Hour()

	return
}

func (m *Muxer) OpenMP4() (err error) {
	m.WriteTrailer()
	m.start = time.Now().UTC()

	p := m.filePatch()
	if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return
	}
	if m.d, err = os.Create(filepath.Dir(p) + "/tmp.mp4"); err != nil {
		return
	}
	m.muxer = mp4.NewMuxer(m.d)
	if err = m.muxer.WriteHeader(m.gof.Streams); err != nil {
		return
	}

	return
}

func (m *Muxer) filePatch() string {
	ts := m.patch
	m.end = time.Now().UTC()
	for _, s := range listTag {
		switch s {
		case "{server_id}":
			ts = strings.Replace(ts, "{host_name}", m.serverID, -1)
		case "{hostname_short}":
			ts = strings.Replace(ts, "{host_name}", m.hostname, -1)
		case "{hostname_long}":
			ts = strings.Replace(ts, "{host_name}", m.hostname, -1)
		case "{stream_name}":
			ts = strings.Replace(ts, "{stream_name}", m.streamName, -1)
		case "{channel_name}":
			ts = strings.Replace(ts, "{channel_name}", m.channelName, -1)
		case "{stream_id}":
			ts = strings.Replace(ts, "{stream_id}", m.streamID, -1)
		case "{channel_id}":
			ts = strings.Replace(ts, "{channel_id}", m.channelID, -1)
		case "{start_year}":
			ts = strings.Replace(ts, "{start_year}", fmt.Sprintf("%d", m.start.Year()), -1)
		case "{start_month}":
			ts = strings.Replace(ts, "{start_month}", fmt.Sprintf("%d", int(m.start.Month())), -1)
		case "{start_day}":
			ts = strings.Replace(ts, "{start_day}", fmt.Sprintf("%d", m.start.Day()), -1)
		case "{start_minute}":
			ts = strings.Replace(ts, "{start_minute}", fmt.Sprintf("%d", m.start.Minute()), -1)
		case "{start_second}":
			ts = strings.Replace(ts, "{start_second}", fmt.Sprintf("%d", m.start.Second()), -1)
		case "{start_millisecond}":
			ts = strings.Replace(ts, "{start_millisecond}", fmt.Sprintf("%d", m.start.Nanosecond()/1000/1000), -1)
		case "{start_unix_millisecond}":
			ts = strings.Replace(ts, "{start_unix_millisecond}", fmt.Sprintf("%d", m.end.UnixMilli()), -1)
		case "{start_unix_second}":
			ts = strings.Replace(ts, "{start_unix_second}", fmt.Sprintf("%d", m.end.Unix()), -1)
		case "{start_time}":
			ts = strings.Replace(ts, "{start_time}", fmt.Sprintf("%s", m.start.Format("2006-01-02T15:04:05-0700")), -1)
		case "{start_pts}":
			ts = strings.Replace(ts, "{start_pts}", fmt.Sprintf("%d", m.pstart.Milliseconds()), -1)
		case "{end_year}":
			ts = strings.Replace(ts, "{end_year}", fmt.Sprintf("%d", m.end.Year()), -1)
		case "{end_month}":
			ts = strings.Replace(ts, "{end_month}", fmt.Sprintf("%d", int(m.end.Month())), -1)
		case "{end_day}":
			ts = strings.Replace(ts, "{end_day}", fmt.Sprintf("%d", m.end.Day()), -1)
		case "{end_minute}":
			ts = strings.Replace(ts, "{end_minute}", fmt.Sprintf("%d", m.end.Minute()), -1)
		case "{end_second}":
			ts = strings.Replace(ts, "{end_second}", fmt.Sprintf("%d", m.end.Second()), -1)
		case "{end_millisecond}":
			ts = strings.Replace(ts, "{end_millisecond}", fmt.Sprintf("%d", m.start.Nanosecond()/1000/1000), -1)
		case "{end_unix_millisecond}":
			ts = strings.Replace(ts, "{end_unix_millisecond}", fmt.Sprintf("%d", m.end.UnixMilli()), -1)
		case "{end_unix_second}":
			ts = strings.Replace(ts, "{end_unix_second}", fmt.Sprintf("%d", m.end.Unix()), -1)
		case "{end_time}":
			ts = strings.Replace(ts, "{end_time}", fmt.Sprintf("%s", m.end.Format("2006-01-02T15:04:05-0700")), -1)
		case "{end_pts}":
			ts = strings.Replace(ts, "{end_pts}", fmt.Sprintf("%d", m.pend.Milliseconds()), -1)
		case "{duration_second}":
			ts = strings.Replace(ts, "{duration_second}", fmt.Sprintf("%f", m.dur.Seconds()), -1)
		case "{duration_millisecond}":
			ts = strings.Replace(ts, "{duration_millisecond}", fmt.Sprintf("%d", m.dur.Milliseconds()), -1)
		}
	}

	return ts
}

func (m *Muxer) WriteTrailer() (err error) {
	if m.muxer != nil {
		m.muxer.WriteTrailer()
	}
	if m.m != nil {
		err = m.m.Close()
	}
	if m.d != nil {
		if m.format == MP4 {
			p := m.filePatch()
			if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				return
			}
			if err = os.Rename(m.d.Name(), p); err != nil {
				return
			}
		}
		err = m.d.Close()
	}

	return
}
