#include <xrpl/basics/MathUtilities.h>

#include <iostream>
#include <string>
#include <utility>
#include <vector>

int main()
{
    using I = std::int64_t;
    constexpr auto max = std::numeric_limits<I>::max();
    constexpr auto min = std::numeric_limits<I>::min();
    std::vector<std::pair<std::string, std::vector<I>>> const cases{
        {"empty", {}},
        {"singleton_max", {max}},
        {"singleton_min", {min}},
        {"zero", {0, 0}},
        {"cancel", {1, -1}},
        {"negative", {-5, 2}},
        {"positive_overflow", {max, 1}},
        {"negative_overflow", {min, -1}},
        {"maximum", {max - 1, 1}},
        {"minimum", {min + 1, -1}},
        {"opposite_extrema", {min, max}},
        {"reversed_extrema", {max, min}},
        {"twice_maximum", {max, max}},
        {"twice_minimum", {min, min}},
        {"maximum_plus_zero", {max, 0}},
        {"minimum_plus_zero", {min, 0}},
        {"multiple_maximum", {max - 2, 1, 1}},
        {"multiple_minimum", {min + 2, -1, -1}},
        {"multiple_positive_overflow", {max - 1, 1, 1}},
        {"multiple_negative_overflow", {min + 1, -1, -1}},
        {"sorted_cancellation", {max, 1, -1}},
        {"sorted_intermediate_overflow", {max, min, -1}},
        {"book_input", {7'500'000'000'000'000'000, 7'500'000'000'000'000'000}},
    };
    std::cout << "{\n  \"oracle\": \"XRPLF/xrpld-private\",\n"
              << "  \"commit\": \"d147fccf54a500fce586522f28d6044c37fd8d29\",\n"
              << "  \"cases\": [\n";
    bool first = true;
    for (auto const& [name, values] : cases)
    {
        if (!std::exchange(first, false))
            std::cout << ",\n";
        std::cout << "    {\"name\": \"" << name << "\", \"values\": [";
        for (std::size_t i = 0; i < values.size(); ++i)
            std::cout << (i ? ", " : "") << '"' << values[i] << '"';
        auto sorted = values;
        std::sort(sorted.begin(), sorted.end());
        std::optional<I> total = sorted.empty() ? 0 : sorted.front();
        for (std::size_t i = 1; i < sorted.size() && total; ++i)
            total = xrpl::checkedAdd(*total, sorted[i]);
        std::cout << "], \"sum\": ";
        if (total)
            std::cout << '"' << *total << '"';
        else
            std::cout << "null";
        std::cout << '}';
    }
    std::cout << "\n  ]\n}\n";
}
