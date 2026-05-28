Terminate a background shell process.

<usage>
- Provide the shell ID returned from a background bash execution
- Cancels the running process and cleans up resources
- If the process does not exit within 20 seconds after being killed, returns an error indicating the process may be unkillable
</usage>

<features>
- Stop long-running background processes
- Clean up completed background shells
- Sends SIGINT then SIGKILL via the shell interpreter on cancellation
- Times out after 20 seconds if the process refuses to exit (e.g. zombie, NFS hang)
</features>

<tips>
- Use this when you need to stop a background process
- The process is sent SIGINT first, then SIGKILL after a grace period
- If the kill times out, the process may be unkillable — it could be a zombie or stuck in an uninterruptible I/O wait
- After killing, the shell ID becomes invalid
</tips>
