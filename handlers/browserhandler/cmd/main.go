package main

import (
	"github.com/njasm/marionette_client"
)

func main() {
	client := marionette_client.NewClient()

	err := client.Connect("", 0)
	if err != nil {
		panic(err)
	}

	_, err = client.NewSession("", nil)
	if err != nil {
		panic(err)
	}

	client.Navigate("https://boss.local/")
	client.DeleteSession()
}
