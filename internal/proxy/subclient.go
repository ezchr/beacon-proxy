package proxy

import "github.com/sandertv/gophertunnel/minecraft/protocol/login"

// ParseLoginRequest reads and verifies a login request (a split-screen player's SubClientLogin), as
// the connection's own Login was.
func (c *ProxyConn) ParseLoginRequest(request []byte) (login.IdentityData, login.ClientData, login.AuthResult, error) {
	return login.Parse(request, c.oidcVerifier)
}
