# HTTPS for joining by address

When a player adds your server by its address, their game asks for `https://` first and only
tries plain `http://` after about 20 seconds. Without HTTPS, everyone who joins by your domain
waits those 20 seconds, and the server list shows nothing until then. Give Beacon a certificate,
in one of two ways:

```toml
bind = "0.0.0.0:19132"
domain = "play.example.com"     # a free certificate from Let's Encrypt, renewed by Beacon
```

`domain` needs the domain to point at this machine and **port 80 free** on it: Let's Encrypt checks
the domain there before issuing or renewing. Certificates are kept in the `certs` folder. If a web
server already uses port 80, set `acme-http-address = "127.0.0.1:8402"` under `[advanced]` and have
the web server forward `/.well-known/acme-challenge/` for your domain to that address. With nginx,
keep the domain in the request:

```nginx
location /.well-known/acme-challenge/ {
    proxy_set_header Host $host;
    proxy_pass http://127.0.0.1:8402;
}
```

```toml
bind = "0.0.0.0:19132"
tls-cert = "/etc/letsencrypt/live/play.example.com/fullchain.pem"
tls-key = "/etc/letsencrypt/live/play.example.com/privkey.pem"
```

`tls-cert` and `tls-key` use a certificate you already have, for example from certbot. Beacon
reads the files again when they are renewed.

Plain HTTP keeps working on the same port, so players who type a bare IP address (which no
certificate can cover) still get in, after the wait.

You can also leave HTTPS to a web server in front of Beacon (nginx, Caddy): bind Beacon to
`127.0.0.1` and have the web server forward `/v1/join` to it.
