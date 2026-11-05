# Development tools

Small programs used while developing and testing Beacon. They are not part of a release and may
assume a particular setup: read a tool's doc comment, then run it with `go run ./tools/<name>`.

| Tool | What it does |
|---|---|
| backendprobe | logs a made-up player into a backend the way Beacon does |
| netbot | test client that joins through Beacon in relay mode |
| nntest | dials a NetherNet backend once and reports what happened |
| cpkcheck, vident | NetherNet identity and SDP signature checks |
| friends, rel, gamertags, friendaudit, unfriend, acceptfriend | inspect or change an Xbox account's friends |
| cbedit | edits command blocks on a running BDS |
