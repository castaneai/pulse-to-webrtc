#!/bin/sh
pulseaudio --daemon --exit-idle-time=-1
"$@"
