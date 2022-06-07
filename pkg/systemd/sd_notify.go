package systemd

import (
	"errors"
	"net"
	"os"
)

var SdNotifyNoSocket = errors.New("No socket")

// Send a message to the init daemon. It is common to ignore the error.
// systemd-notify [OPTIONS...] [VARIABLE=VALUE...]
//Notify the init system about service status updates.
func SdNotify(state string) error {
	socketAddr := &net.UnixAddr{
		Name: os.Getenv("NOTIFY_SOCKET"),
		Net:  "unixgram",
	}

	if socketAddr.Name == "" {
		return SdNotifyNoSocket
	}
	// unix套接字通知init
	conn, err := net.DialUnix(socketAddr.Net, nil, socketAddr)
	if err != nil {
		return err
	}

	_, err = conn.Write([]byte(state))
	if err != nil {
		return err
	}

	return nil
}
