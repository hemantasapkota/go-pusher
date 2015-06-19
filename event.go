package pusher

type ConnectionData struct {
	SocketId        string `json:"socket_id"`
	ActivityTimeout int    `'json:"activity_timeout"`
}

type Event struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

// eventError represent a pusher:error data
type eventError struct {
	message string `json:"message"`
	code    int    `json:"code"`
}
