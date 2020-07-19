default:
	env GOOS=linux GOARCH=arm GOARM=7 go build -mod=vendor -o pumpswitch7.arm ./main.go
pi6:
	env GOOS=linux GOARCH=arm GOARM=6 go build -mod=vendor -o pumpswitch6.arm ./main.go
