package protocol

const (
	ServiceGateway uint16 = 1
	ServiceAuth    uint16 = 2
	ServiceLobby   uint16 = 3
	ServiceMatch   uint16 = 4
	ServiceRoom    uint16 = 5
)

type RouteEnvelope struct {
	ServiceID uint16         `json:"service_id"`
	MsgID     uint16         `json:"msg_id"`
	Body      map[string]any `json:"body,omitempty"`
}
