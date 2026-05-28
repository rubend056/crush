Get stdout/stderr from a background shell by ID; set wait=true to block until completion.

<usage>
- Provide the shell ID returned from a background bash execution
- Returns the current stdout and stderr output
- Indicates whether the shell has completed execution
- Set wait=true to block until the shell completes (times out after 5 minutes)
</usage>

<features>
- View output from running background processes
- Check if background process has completed
- Get cumulative output from process start
- Optionally wait for process completion with a 5-minute timeout
</features>

<tips>
- Use this to monitor long-running processes
- Check the 'done' status to see if process completed
- Can be called multiple times to view incremental output
- Use wait=true when you need the final output and exit status
- If the process takes longer than 5 minutes, the wait times out and returns whatever output is available so far
</tips>
