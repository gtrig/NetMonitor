package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gtrig/netmon/internal/config"
)

func raw() *config.Config {
	return &config.Config{
		Listen: "0.0.0.0:8080",
		Auth:   &config.BasicAuth{Username: "admin", Password: "pw"},
		Routers: []config.Router{
			{Name: "core", Type: "mikrotik", Address: "10.0.0.1:8728", Username: "ro", Password: "${CORE_PASS}"},
			{Name: "ap", Type: "mikrotik", Address: "10.0.0.2:8728", Username: "ro", Password: "literal-secret"},
		},
		Channels: []config.Channel{
			{Name: "tg", Type: "telegram", BotToken: "123:abc", ChatID: "42"},
			{Name: "dc", Type: "discord", WebhookURL: "${DISCORD_WEBHOOK_URL}"},
		},
		SNMPTargets: []config.SNMPTarget{{Name: "sw", Address: "10.0.0.3", Version: "v2c", Community: "public"}},
	}
}

func TestMaskHidesLiteralSecretsAndShowsReferences(t *testing.T) {
	e := Mask(raw())
	b, _ := json.Marshal(e)
	out := string(b)
	for _, secret := range []string{"literal-secret", "123:abc", "public", "pw"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked: %s", secret, out)
		}
	}
	if e.Routers[0].Password != "${CORE_PASS}" || e.Routers[1].Password != Masked {
		t.Fatalf("routers = %+v", e.Routers)
	}
	if e.Channels[1].WebhookURL != "${DISCORD_WEBHOOK_URL}" || e.Channels[0].ChatID != "42" {
		t.Fatalf("channels = %+v", e.Channels)
	}
	if e.Routers[1].OriginalName != "ap" {
		t.Fatalf("original name not set: %+v", e.Routers[1])
	}
}

func TestApplyKeepsMaskedAndEmptySecrets(t *testing.T) {
	r := raw()
	e := Mask(r)
	e.Routers[1].Password = Masked
	e.Channels[0].BotToken = ""
	out := Apply(r, e)
	if out.Routers[1].Password != "literal-secret" || out.Channels[0].BotToken != "123:abc" {
		t.Fatalf("secrets lost: %+v %+v", out.Routers[1], out.Channels[0])
	}
	if out.Auth == nil || out.Listen != "0.0.0.0:8080" {
		t.Fatal("non-editable fields changed")
	}
	if out.Routers[0].OriginalName != "" {
		t.Fatal("OriginalName leaked into config")
	}
}

func TestApplyRenameKeepsSecret(t *testing.T) {
	r := raw()
	e := Mask(r)
	e.Routers[1].Name = "attic"
	out := Apply(r, e)
	if out.Routers[1].Name != "attic" || out.Routers[1].Password != "literal-secret" {
		t.Fatalf("router = %+v", out.Routers[1])
	}
}

func TestApplyNewSecretAndNewItem(t *testing.T) {
	r := raw()
	e := Mask(r)
	e.Channels[0].BotToken = "999:new"
	e.Routers = append(e.Routers, config.Router{Name: "lab", Type: "mikrotik", Address: "10.0.9.1:8728", Password: "${LAB_PASS}"})
	out := Apply(r, e)
	if out.Channels[0].BotToken != "999:new" || out.Routers[2].Password != "${LAB_PASS}" {
		t.Fatalf("out = %+v / %+v", out.Channels[0], out.Routers[2])
	}
	if r.Channels[0].BotToken != "123:abc" {
		t.Fatal("Apply mutated the original")
	}
}

func TestApplyNewItemCannotStealSecretViaOriginalName(t *testing.T) {
	r := raw()
	e := Mask(r)
	// A brand-new item claiming to be "ap" only inherits if "ap" is not also still present.
	e.Routers = append(e.Routers, config.Router{Name: "x", OriginalName: "ap", Type: "mikrotik", Address: "1.1.1.1:8728", Password: Masked})
	out := Apply(r, e)
	if out.Routers[2].Password != "" {
		t.Fatalf("duplicate claim inherited secret: %+v", out.Routers[2])
	}
}

func TestNetworkRenames(t *testing.T) {
	r := raw()
	r.Networks = []config.Network{{Name: "lan"}, {Name: "vpn"}}
	e := Mask(r)
	if e.Networks[0].OriginalName != "lan" {
		t.Fatalf("Mask did not set network original name: %+v", e.Networks)
	}
	e.Networks[0].Name = "GTLan"                                                  // rename
	e.Networks = append(e.Networks, config.Network{Name: "x", OriginalName: "lan"}) // second claimant
	e.Networks = append(e.Networks, config.Network{Name: "new"})                  // new item
	got := NetworkRenames(r, e)
	if len(got) != 1 || got["lan"] != "GTLan" {
		t.Fatalf("renames = %v", got)
	}
	for _, n := range Apply(r, e).Networks {
		if n.OriginalName != "" {
			t.Fatalf("Apply kept original name: %+v", n)
		}
	}
}
