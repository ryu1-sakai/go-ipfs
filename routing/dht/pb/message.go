package dht_pb

import (
	ma "gx/ipfs/QmcobAGsCjYt5DXoq9et9L8yR8er7o7Cu3DTvpaq12jYSz/go-multiaddr"

	key "github.com/ipfs/go-ipfs/blocks/key"
	inet "gx/ipfs/QmXDvxcXUYn2DDnGKJwdQPxkJgG83jBTp5UmmNzeHzqbj5/go-libp2p/p2p/net"
	peer "gx/ipfs/QmZwZjMVGss5rqYsJVGy18gNbkTJffFyq2x1uJ4e4p3ZAt/go-libp2p-peer"
	logging "gx/ipfs/Qmazh5oNUVsDZTs2g59rq8aYQqwpss8tcUWQzor5sCCEuH/go-log"
)

var log = logging.Logger("dht.pb")

type PeerRoutingInfo struct {
	peer.PeerInfo
	inet.Connectedness
}

// NewMessage constructs a new dht message with given type, key, and level
func NewMessage(typ Message_MessageType, key string, level int) *Message {
	m := &Message{
		Type: &typ,
		Key:  &key,
	}
	m.SetClusterLevel(level)
	return m
}

func peerRoutingInfoToPBPeer(p PeerRoutingInfo) *Message_Peer {
	pbp := new(Message_Peer)

	pbp.Addrs = make([][]byte, len(p.Addrs))
	for i, maddr := range p.Addrs {
		pbp.Addrs[i] = maddr.Bytes() // Bytes, not String. Compressed.
	}
	s := string(p.ID)
	pbp.Id = &s
	c := ConnectionType(p.Connectedness)
	pbp.Connection = &c
	return pbp
}

func peerInfoToPBPeer(p peer.PeerInfo) *Message_Peer {
	pbp := new(Message_Peer)

	pbp.Addrs = make([][]byte, len(p.Addrs))
	for i, maddr := range p.Addrs {
		pbp.Addrs[i] = maddr.Bytes() // Bytes, not String. Compressed.
	}
	s := string(p.ID)
	pbp.Id = &s
	return pbp
}

// PBPeerToPeer turns a *Message_Peer into its peer.PeerInfo counterpart
func PBPeerToPeerInfo(pbp *Message_Peer) peer.PeerInfo {
	return peer.PeerInfo{
		ID:    peer.ID(pbp.GetId()),
		Addrs: pbp.Addresses(),
	}
}

// RawPeerInfosToPBPeers converts a slice of Peers into a slice of *Message_Peers,
// ready to go out on the wire.
func RawPeerInfosToPBPeers(peers []peer.PeerInfo) []*Message_Peer {
	pbpeers := make([]*Message_Peer, len(peers))
	for i, p := range peers {
		pbpeers[i] = peerInfoToPBPeer(p)
	}
	return pbpeers
}

// PeersToPBPeers converts given []peer.Peer into a set of []*Message_Peer,
// which can be written to a message and sent out. the key thing this function
// does (in addition to PeersToPBPeers) is set the ConnectionType with
// information from the given inet.Network.
func PeerInfosToPBPeers(n inet.Network, peers []peer.PeerInfo) []*Message_Peer {
	pbps := RawPeerInfosToPBPeers(peers)
	for i, pbp := range pbps {
		c := ConnectionType(n.Connectedness(peers[i].ID))
		pbp.Connection = &c
	}
	return pbps
}

func PeerRoutingInfosToPBPeers(peers []PeerRoutingInfo) []*Message_Peer {
	pbpeers := make([]*Message_Peer, len(peers))
	for i, p := range peers {
		pbpeers[i] = peerRoutingInfoToPBPeer(p)
	}
	return pbpeers
}

// PBPeersToPeerInfos converts given []*Message_Peer into []peer.PeerInfo
// Invalid addresses will be silently omitted.
func PBPeersToPeerInfos(pbps []*Message_Peer) []peer.PeerInfo {
	peers := make([]peer.PeerInfo, 0, len(pbps))
	for _, pbp := range pbps {
		peers = append(peers, PBPeerToPeerInfo(pbp))
	}
	return peers
}

// Addresses returns a multiaddr associated with the Message_Peer entry
func (m *Message_Peer) Addresses() []ma.Multiaddr {
	if m == nil {
		return nil
	}

	var err error
	maddrs := make([]ma.Multiaddr, len(m.Addrs))
	for i, addr := range m.Addrs {
		maddrs[i], err = ma.NewMultiaddrBytes(addr)
		if err != nil {
			log.Debugf("error decoding Multiaddr for peer: %s", m.GetId())
			continue
		}
	}
	return maddrs
}

// GetClusterLevel gets and adjusts the cluster level on the message.
// a +/- 1 adjustment is needed to distinguish a valid first level (1) and
// default "no value" protobuf behavior (0)
func (m *Message) GetClusterLevel() int {
	level := m.GetClusterLevelRaw() - 1
	if level < 0 {
		return 0
	}
	return int(level)
}

// SetClusterLevel adjusts and sets the cluster level on the message.
// a +/- 1 adjustment is needed to distinguish a valid first level (1) and
// default "no value" protobuf behavior (0)
func (m *Message) SetClusterLevel(level int) {
	lvl := int32(level)
	m.ClusterLevelRaw = &lvl
}

// Loggable turns a Message into machine-readable log output
func (m *Message) Loggable() map[string]interface{} {
	return map[string]interface{}{
		"message": map[string]string{
			"type": m.Type.String(),
			"key":  key.Key(m.GetKey()).B58String(),
		},
	}
}

// ConnectionType returns a Message_ConnectionType associated with the
// inet.Connectedness.
func ConnectionType(c inet.Connectedness) Message_ConnectionType {
	switch c {
	default:
		return Message_NOT_CONNECTED
	case inet.NotConnected:
		return Message_NOT_CONNECTED
	case inet.Connected:
		return Message_CONNECTED
	case inet.CanConnect:
		return Message_CAN_CONNECT
	case inet.CannotConnect:
		return Message_CANNOT_CONNECT
	}
}

// Connectedness returns an inet.Connectedness associated with the
// Message_ConnectionType.
func Connectedness(c Message_ConnectionType) inet.Connectedness {
	switch c {
	default:
		return inet.NotConnected
	case Message_NOT_CONNECTED:
		return inet.NotConnected
	case Message_CONNECTED:
		return inet.Connected
	case Message_CAN_CONNECT:
		return inet.CanConnect
	case Message_CANNOT_CONNECT:
		return inet.CannotConnect
	}
}
