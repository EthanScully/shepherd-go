package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	go func() {
		err = service(cli, ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error running service: %v", err)
		}
		for {
			if cronJob() {
				err = service(cli, ctx)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error running service: %v", err)
				}
			}
			time.Sleep(time.Second * 59)
		}
	}()
	inter := make(chan os.Signal, 1)
	signal.Notify(inter, os.Interrupt, syscall.SIGTERM)
	<-inter
}
func cronJob() (start bool) {
	if len(os.Args) < 1 {
		time.Sleep(time.Minute * 4)
		return true
	}
	now := time.Now()
	cron := os.Args[1:]
	run := 0
	cronCheck := func(cron []string, pos, time int) (int, error) {
		if len(cron) > pos && len(cron[pos]) > 0 {
			num, err := strconv.Atoi(cron[pos])
			if err != nil {
				if cron[pos][0] == '*' {
					if i := strings.Index(cron[pos], "/"); i != -1 && len(cron[pos]) > i+1 {
						num, err := strconv.Atoi(cron[pos][i+1:])
						if err != nil {
							return 0, fmt.Errorf("invalid cron format")
						} else if time%num == 0 {
							return 1 << pos, nil
						} else {
							return 0, nil
						}
					}
					return 1 << pos, nil
				}
				return 0, fmt.Errorf("invalid cron format")
			} else if time == num {
				return 1 << pos, nil
			}
		}
		return 0, nil
	}
	// minute
	out, err := cronCheck(cron, 0, now.Minute())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	run = run | out
	//hour
	out, err = cronCheck(cron, 1, now.Hour())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	run = run | out
	//day
	out, err = cronCheck(cron, 2, now.Day())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	run = run | out
	//month
	out, err = cronCheck(cron, 3, int(now.Month()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	run = run | out
	//weekday
	out, err = cronCheck(cron, 4, int(now.Weekday()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	run = run | out
	if run == 31 {
		start = true
		time.Sleep(time.Second)
	}
	return
}
func getAuth() (auths []registry.AuthConfig, err error) {
	config, err := os.ReadFile("/root/.docker/config.json")
	if err != nil {
		err = fmt.Errorf("docker config not found, no registry authorization will be used, %v", err)
		return
	}
	configJson := make(map[string]map[string]map[string]string)
	err = json.Unmarshal(config, &configJson)
	for k, v := range configJson["auths"] {
		decode, er := base64.StdEncoding.DecodeString(v["auth"])
		if er != nil {
			fmt.Fprintln(os.Stderr, er)
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
		fmt.Fprintln(os.Stderr, err)
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
		// Get Tag
		tag := service.Spec.TaskTemplate.ContainerSpec.Image
		atIndex := strings.LastIndex(tag, "@")
		if atIndex != -1 {
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
					fmt.Fprintf(os.Stderr, "error encoding auth: %v\n", err)
					continue
				}
				break
			}
		}
		// Pull Tag
		ret, err := cli.ImagePull(ctx, tag, image.PullOptions{
			RegistryAuth: auth,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error pulling image: %v\n", err)
			continue
		}
		retData, err := io.ReadAll(ret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading pull response: %v\n", err)
			continue
		}
		ret.Close()
		if strings.Contains(string(retData), "up to date") {
			continue
		}
		// Update Service
		fmt.Printf("Updating Service: %s\n", tag)
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
