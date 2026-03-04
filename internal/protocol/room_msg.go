package protocol

const (
	RoomMsgCreateRoom  uint16 = 1001
	RoomMsgJoinRoom    uint16 = 1002
	RoomMsgReady       uint16 = 1003
	RoomMsgLeaveRoom   uint16 = 1004
	RoomMsgStartGame   uint16 = 1005
	RoomMsgSubmitInput uint16 = 1101
	RoomMsgPullInputs  uint16 = 1102
	RoomMsgSnapshot    uint16 = 1201
	RoomMsgListRooms   uint16 = 1202
)
