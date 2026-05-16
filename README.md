# ActionsCat
Status: early prototype

## Build

Requires Go 1.25+.

```powershell
# from project root
go build ./...
```

## Run

```powershell
# set the listen address if needed
$env:ACTIONSCAT_ADDR = ":8080"
go run main.go
```

## Contributing
See `CONTRIBUTING.md`.

## License
MPL-2.0 (see LICENSE file)
