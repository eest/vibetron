How to build:
```
docker build . -t eest/vibetron:x.y.z --build-arg=VIBETRON_VERSION=x.y.z
```

How to publish:
```
$ docker push eest/vibetron:x.y.z
```

How to create secret:
```
$ echo "secret token" | docker secret create vibetron_token -
```

How to deploy service:
```
$ docker service create \
    --name vibetron \
    --replicas 1 \
    --secret vibetron_token \
    eest/vibetron:x.y.z
```

How to update deployment:
```
docker service update --image eest/vibetron:x.y.z <service ID>
```
