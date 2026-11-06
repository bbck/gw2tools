# gw2tools

A collection of utilities for the game Guild Wars 2.

## Tools

- Guaranteed wardrobe unlock TP tracking
- Black Lion weapon set completion costs

## Running

You need [mise](https://mise.jdx.dev) and a [GW2 API key](https://account.arena.net/applications) with the `account`, `unlocks` and `tradingpost` permissions.

```sh
mise install
mise run build
./bin/gw2tools seed
GW2_API_KEY=your-key ./bin/gw2tools serve
```

## Credits

- Guaranteed wardrobe unlock data comes from immortius' [Wardrobe Unlock Analyser](https://github.com/immortius/gw2-wardrobe-unlock)
- Current Black Lion Chest contents come from the [Guild Wars 2 Wiki](https://wiki.guildwars2.com/wiki/Black_Lion_Chest)
