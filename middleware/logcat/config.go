package logcat

type Config struct {
	Backend string
}

var ConfigDefault = Config{
	Backend: "127.0.0.1:51151",
}
