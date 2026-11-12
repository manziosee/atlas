namespace Atlas.Client.Tests;

/// <summary>The node the live tests run against; CI starts one with scripts/start-test-node.sh.</summary>
internal static class LiveNode
{
    public static readonly string Endpoint = Environment.GetEnvironmentVariable("ATLAS_TEST_ENDPOINT") ?? "";
    public static readonly string Token = Environment.GetEnvironmentVariable("ATLAS_TEST_TOKEN") ?? "";

    public static bool Configured => Endpoint.Length > 0 && Token.Length > 0;

    public static AtlasClient Connect() => new(Endpoint, Token);

    public static string Unique(string prefix) => prefix + "-" + Guid.NewGuid().ToString("N")[..8];
}

/// <summary>A test that needs a running node; skipped when ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN are unset.</summary>
public sealed class LiveFactAttribute : FactAttribute
{
    public LiveFactAttribute()
    {
        if (!LiveNode.Configured)
        {
            Skip = "set ATLAS_TEST_ENDPOINT and an admin ATLAS_TEST_TOKEN to run live tests";
        }
    }
}
