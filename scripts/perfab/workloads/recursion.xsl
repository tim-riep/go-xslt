<!-- deep recursion: a named template that walks the record list one call per record, 5,000 levels deep -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:template name="walk" as="xs:decimal">
    <xsl:param name="nodes" as="element(order)*"/>
    <xsl:param name="acc" as="xs:decimal"/>
    <xsl:choose>
      <xsl:when test="empty($nodes)"><xsl:sequence select="$acc"/></xsl:when>
      <xsl:otherwise>
        <xsl:call-template name="walk">
          <xsl:with-param name="nodes" select="subsequence($nodes, 2)"/>
          <xsl:with-param name="acc" select="$acc + xs:decimal($nodes[1]/price)"/>
        </xsl:call-template>
      </xsl:otherwise>
    </xsl:choose>
  </xsl:template>
  <xsl:template match="/">
    <xsl:variable name="orders" as="element(order)*" select="orders/order"/>
    <xsl:for-each select="1 to 40">
      <xsl:variable name="i" select="."/>
      <xsl:call-template name="walk">
        <xsl:with-param name="nodes" select="subsequence($orders, ($i - 1) * 500 + 1, 500)"/>
        <xsl:with-param name="acc" select="0"/>
      </xsl:call-template>
      <xsl:text>&#10;</xsl:text>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>
