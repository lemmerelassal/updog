# updog

# description

updog is a daemon that automatically uploads a plugged in thumbdrive to a specified sftp server.

# install

execute the following in terminal

```
sudo cp com.lemmerelassal.updog.plist /Library/LaunchDaemons/com.lemmerelassal.updog.plist
sudo chown root /Library/LaunchDaemons/com.lemmerelassal.updog.plist
sudo cp updog.json /etc/updog.json
sudo cp updog /usr/local/bin/updog
sudo launchctl load /Library/LaunchDaemons/com.lemmerelassal.updog.plist
```

# logs

To view logs:

```
tail -f /tmp/updog.\*
```
