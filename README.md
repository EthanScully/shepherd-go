Docker swarm service for automatically updating your services whenever their image is refreshed


### Usage
example in docker compose:

```YAML
services:
  shepherd:
    image: ethanscully/shepherd
    deploy:
      mode: global
    volumes: 
      - /var/run/docker.sock:/var/run/docker.sock
      - /root/.docker/config.json:/root/.docker/config.json:ro
    command: 0 5 * * *      ### Optional Cron Option
