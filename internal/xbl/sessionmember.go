package xbl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ezchr/beacon-proxy/internal/session"
	"github.com/google/uuid"
)

// Joining a session as a member without joining its world, and checking who is in it: what a
// world's inviters and members do (cmd/beacon/sessionmember.go).

func sessionRefURL(r SessionRef) string {
	return fmt.Sprintf("https://sessiondirectory.xboxlive.com/serviceconfigs/%s/sessionTemplates/%s/sessions/%s",
		r.Scid, r.TemplateName, url.PathEscape(r.Name))
}

// sessionDoc is the part of a session document Beacon reads: its custom properties and its members.
type sessionDoc struct {
	Properties struct {
		Custom SessionCustomProperties `json:"custom"`
	} `json:"properties"`
	Members map[string]struct {
		Gamertag  string `json:"gamertag"`
		Constants struct {
			System struct {
				Xuid string `json:"xuid"`
			} `json:"system"`
		} `json:"constants"`
	} `json:"members"`
}

func (d sessionDoc) hasMember(xuid string) bool {
	for _, m := range d.Members {
		if m.Constants.System.Xuid == xuid {
			return true
		}
	}
	return false
}

// readSession reads the session document at sessionURL.
func readSession(ctx context.Context, authSession *session.Session, sessionURL string) (sessionDoc, error) {
	var doc sessionDoc
	auth, err := activityAuthHeader(ctx, authSession)
	if err != nil {
		return doc, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionURL, nil)
	if err != nil {
		return doc, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("x-xbl-contract-version", xblContractVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return doc, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return doc, fmt.Errorf("read session: status %d: %s", resp.StatusCode, string(b))
	}
	return doc, json.Unmarshal(b, &doc)
}

// HasMember reports whether xuid is still a member of the joined session.
func (j *JoinedSession) HasMember(ctx context.Context, xuid string) (bool, error) {
	doc, err := readSession(ctx, j.auth, j.url)
	if err != nil {
		return false, err
	}
	return doc.hasMember(xuid), nil
}

// JoinSessionRef joins the session ref directly, as a reserved (invited) member may. See JoinSession
// for connectionID.
func JoinSessionRef(ctx context.Context, authSession *session.Session, xuid, connectionID string, ref SessionRef) (*JoinedSession, error) {
	auth, err := activityAuthHeader(ctx, authSession)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(joinSessionRequest{
		Members: map[string]*SessionMember{
			"me": {
				Constants: map[string]MemberConstantsSystem{"system": {Xuid: xuid, Initialize: true}},
				Properties: map[string]MemberPropertiesSystem{
					"system": {
						Active:     true,
						Connection: connectionID,
						Subscription: MemberSubscription{
							ID:          strings.ToUpper(uuid.NewString()),
							ChangeTypes: []string{"everything"},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionRefURL(ref), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	req.Header.Set("x-xbl-contract-version", xblContractVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("join session: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("join session: status %d: %s", resp.StatusCode, string(respBody))
	}
	return &JoinedSession{auth: authSession, url: sessionRefURL(ref)}, nil
}
