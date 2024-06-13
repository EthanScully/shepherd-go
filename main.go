package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
)

func main() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(fmt.Errorf("error connecting to docker socket: %v", err))
	}
	defer cli.Close()
	err = service(cli, ctx)
	if err != nil {
		panic(fmt.Errorf("error running service: %v", err))
	}
}

func getAuth() (auths []registry.AuthConfig, err error) {
	config, err := os.ReadFile("/root/.docker/config.json")
	if err != nil {
		return
	}
	configJson := make(map[string]map[string]map[string]string)
	err = json.Unmarshal(config, &configJson)
	for k, v := range configJson["auths"] {
		decode, err := base64.StdEncoding.DecodeString(v["auth"])
		if err != nil {
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
	fmt.Printf("Space Reclaimed: %.1fMB\n", float64(pruneReport.SpaceReclaimed)/1e6)
	return
}
func service(cli *client.Client, ctx context.Context) (err error) {
	auths, err := getAuth()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker config not found, no registry authorization will be used, %v", err)
	}
	err = prune(cli, ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Image Prune Failed: %v\n", err)
	}
	services, err := cli.ServiceList(ctx, types.ServiceListOptions{})
	if err != nil {
		return
	}
	for _, service := range services {
		tag := service.Spec.TaskTemplate.ContainerSpec.Image
		atIndex := strings.LastIndex(tag, "@")
		if atIndex != -1 {
			tag = tag[:atIndex]
		}
		img, _, err := cli.ImageInspectWithRaw(ctx, tag)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		digests := make(map[string]bool)
		for _, digest := range img.RepoDigests {
			digests[digest] = true
		}
		var auth string
		platform := "docker.io"
		if strings.Count(tag, "/") > 1 {
			platform = tag[:strings.Index(tag, "/")]
		}
		for _, v := range auths {
			if strings.Contains(v.ServerAddress, platform) {
				auth, err = registry.EncodeAuthConfig(v)
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					continue
				}
				break
			}
		}
		ret, err := cli.ImagePull(ctx, tag, image.PullOptions{
			RegistryAuth: auth,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			goto update
		}
		ret.Close()
		img, _, err = cli.ImageInspectWithRaw(ctx, tag)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			goto update
		}
		for _, digest := range img.RepoDigests {
			if !digests[digest] {
				goto update
			}
		}
		continue
	update:
		fmt.Printf("Updating image: %s\n", tag)
		service.Spec.TaskTemplate.ForceUpdate += 1
		service.Spec.TaskTemplate.ContainerSpec.Image = tag
		resp, err := cli.ServiceUpdate(ctx, service.ID, service.Version, service.Spec, types.ServiceUpdateOptions{})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if resp.Warnings != nil && len(resp.Warnings) > 0 {
			for _, v := range resp.Warnings {
				fmt.Printf("Warning: %s\n", v)
			}
		}
	}
	return
}
