package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/robfig/cron/v3"
)

func main() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(fmt.Errorf("error connecting to docker socket: %v", err))
	}
	defer cli.Close()
	wrapper := func() {
		if err := service(cli, ctx); err != nil {
			fmt.Fprintf(os.Stderr, "error running service: %v", err)
		}
	}
	wrapper()
	entry := "0 */4 * * *"
	if len(os.Args) >= 6 {
		entry = strings.Join(os.Args[1:6], " ")
	}
	c := cron.New()
	_, err = c.AddFunc(entry, wrapper)
	if err != nil {
		panic(err)
	}
	c.Start()
	inter := make(chan os.Signal, 1)
	signal.Notify(inter, os.Interrupt, syscall.SIGTERM)
	<-inter
}
func getAuth() (auths []registry.AuthConfig, err error) {
	config, err := os.ReadFile("/root/.docker/config.json")
	if err != nil {
		err = fmt.Errorf("docker config not found, no registry authorization will be used, %s", err.Error())
		return
	}
	configJson := make(map[string]map[string]map[string]string)
	err = json.Unmarshal(config, &configJson)
	if err != nil {
		err = fmt.Errorf("json unmarshal error, %s", err.Error())
		return
	}
	for k, v := range configJson["auths"] {
		decode, err := base64.StdEncoding.DecodeString(v["auth"])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		parameters := strings.Split(string(decode), ":")
		if len(parameters) != 2 {
			continue
		}
		auths = append(auths, registry.AuthConfig{
			Username:      parameters[0],
			Password:      parameters[1],
			ServerAddress: k,
		})

	}
	return
}
func prune(cli *client.Client, ctx context.Context) (err error) {
	pruneReport, err := cli.ImagesPrune(ctx, filters.Args{})
	if err != nil {
		return
	}
	for _, v := range pruneReport.ImagesDeleted {
		if v.Deleted != "" {
			fmt.Printf("Deleted: %s\n", v.Deleted)
		}
		if v.Untagged != "" {
			fmt.Printf("Untagged: %s\n", v.Untagged)
		}
	}
	if pruneReport.SpaceReclaimed > 0 {
		fmt.Printf("Space Reclaimed: %.1fMB\n", float64(pruneReport.SpaceReclaimed)/1e6)
	}
	return
}
func service(cli *client.Client, ctx context.Context) (err error) {
	auths, err := getAuth()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
	}
	err = prune(cli, ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Image Prune Failed: %s\n", err.Error())
	}
	services, err := cli.ServiceList(ctx, types.ServiceListOptions{})
	if err != nil {
		return
	}
	for _, service := range services {
		// Get Tag
		var tagDigest string
		tag := service.Spec.TaskTemplate.ContainerSpec.Image
		atIndex := strings.LastIndex(tag, "@")
		if atIndex != -1 {
			if len(tag) > atIndex+1 {
				tagDigest = tag[atIndex+1:]
			}
			tag = tag[:atIndex]
		}
		// Set Up Auth for Pulling Tag
		platform := "docker.io"
		if strings.Count(tag, "/") > 1 {
			platform = tag[:strings.Index(tag, "/")]
		}
		var auth string
		for _, v := range auths {
			if strings.Contains(v.ServerAddress, platform) {
				auth, err = registry.EncodeAuthConfig(v)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error encoding auth: %s\n", err.Error())
					continue
				}
				break
			}
		}
		// Compare Digests
		DistInsp, err := cli.DistributionInspect(ctx, tag, auth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error contacting remote registry, %s\n", err.Error())
			continue
		}
		if string(DistInsp.Descriptor.Digest) == tagDigest {
			continue
		}
		tag = fmt.Sprintf("%s@%s", tag, string(DistInsp.Descriptor.Digest))
		// Update Service
		fmt.Printf("Updating Service: %s\n", service.Spec.Name)
		service.Spec.TaskTemplate.ContainerSpec.Image = tag
		resp, err := cli.ServiceUpdate(ctx, service.ID, service.Version, service.Spec, types.ServiceUpdateOptions{
			EncodedRegistryAuth: auth,
			RegistryAuthFrom:    "spec",
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if len(resp.Warnings) > 0 {
			for _, v := range resp.Warnings {
				fmt.Printf("Warning: %s\n", v)
			}
		}
	}
	return
}
