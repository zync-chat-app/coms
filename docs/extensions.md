# Building Extensions

Zync comS supports custom extensions via namespaced WebSocket message types. This lets you add any functionality — games, custom UIs, bots, whatever — without modifying the core protocol.

## Namespaces

| Prefix | Owner | Description |
|--------|-------|-------------|
| `zync.core.*` | Zync | Core protocol — do not use |
| `zync.channels.*` | Zync | Official channels extension |
| `zync.roles.*` | Zync | Official roles extension |
| `com.*` | Community | Your custom extensions |

Use reverse-domain notation: `com.yourname.extension.action`

## Adding a handler

Register a handler for your custom message type in your comS:

```go
hub.Register("com.example.chess.move", func(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
    var move struct {
        From string `json:"from"`
        To   string `json:"to"`
    }
    json.Unmarshal(msg.D, &move)

    // process move...

    // broadcast to all clients
    hub.Broadcast(&ws.Message{
        T:  "com.example.chess.move",
        ID: newMsgID(),
        D:  mustJSON(move),
    })

    return nil, nil
})
```

## Declaring extensions in the manifest

Tell clients what extensions you support by adding them to the manifest:

```go
extensions := []manifest.Extension{
    {
        ID:                "com.example.chess",
        Version:           "1.0.0",
        Name:              "Chess",
        MessageNamespaces: []string{"com.example.chess.*"},
    },
}
```

## Custom UI extensions

If your extension provides a UI, you can supply a sandboxed iframe URL:

```go
uiURL := "https://chess-extension.example.com/ui/v1"
manifest.Extension{
    ID:      "com.example.chess",
    Name:    "Chess",
    UIURL:   &uiURL,
    // ...
}
```

The official Zync client will offer to load this URL in a sandboxed iframe. The UI communicates with the client via a restricted `postMessage` API — it cannot access the DOM directly or read other channels.

**Important:** Custom UI extensions require user consent before loading. The client will show a prompt: *"This server wants to load a custom UI from chess-extension.example.com. Allow?"*

## Unknown message types

The official client silently ignores unknown namespaces. Third-party clients and extensions can handle them however they like. This means your extension is fully backwards-compatible — servers that understand it use it, others ignore it.
