be sure to create a "bin" folder in the root of the project "control_grpc/bin" and place ffmpeg and ffplay there
#Upd
ffplay no longer needed. ffmpeg is still needed tho

#Build
run
```bash
buf generate
```
in the root folder (this will update gen/proto)

then
```bash
go mod tidy
```
to install the modules (?)

finally (in both /server and /client folders)
```bash
go run main.go
```
