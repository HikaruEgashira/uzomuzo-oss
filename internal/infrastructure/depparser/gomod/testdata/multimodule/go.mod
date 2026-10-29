module example.com/app

go 1.26.0

require (
 example.com/used v1.0.0
 example.com/unused v1.0.0
 example.com/shared v1.0.0 // indirect
 gopkg.in/DataDog/dd-trace-go.v1 v1.0.0 // indirect
 example.com/tool v1.0.0 // indirect
)
tool example.com/tool/cmd/check
