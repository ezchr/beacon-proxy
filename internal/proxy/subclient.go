package proxy

import "github.com/sandertv/gophertunnel/minecraft/protocol/login"

// ParseLoginRequest reads and verifies a login request (a split-screen player's SubClientLogin), as
// the connection's own Login was. A guest without an account of its own (AuthResult.Guest) is signed
// with the main player's token; client data fields it leaves out are the main player's.
func (c *ProxyConn) ParseLoginRequest(request []byte) (login.IdentityData, login.ClientData, login.AuthResult, error) {
	return login.ParseSubClient(request, c.oidcVerifier, c.clientData)
}
