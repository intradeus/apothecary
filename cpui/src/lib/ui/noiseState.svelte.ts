// The backdrop's live parameters (DESIGN 2.2). A page says what it is about and
// the backdrop follows.
//
// blue: the share of sky among the circles, 0–100. 0 is all ember, 50 half and
//       half, 100 all sky; any change takes three seconds. The run page leans
//       ember while an agent brews and sky while it waits on you.
// speed: how fast the circles orbit, as a multiple of their resting pace.
//        0 stops them.

export const NEUTRAL_MIX = 50;

export const noise = $state({ blue: NEUTRAL_MIX, speed: 1 });
